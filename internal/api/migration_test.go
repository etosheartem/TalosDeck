package api

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"talosdeck/internal/clusters"
)

// Keep every provider/alert fallback confined to fixtures, including installations
// whose shell exports real credentials or whose home has legacy token files.
func isolateMigrationEnvironment(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"PROXMOX_URL", "PVE_URL", "PROXMOX_NODE", "PVE_NODE", "PROXMOX_API_TOKEN", "PVE_API_TOKEN", "PROXMOX_TOKEN", "PROXMOX_USERNAME", "PVE_USERNAME", "PROXMOX_USER", "PROXMOX_PASSWORD", "PVE_PASSWORD", "PROXMOX_STORAGE", "PVE_STORAGE", "PROXMOX_ISO", "PVE_ISO", "PROXMOX_BRIDGE", "PVE_BRIDGE", "PROXMOX_SKIP_TLS_VERIFY", "PVE_SKIP_TLS_VERIFY", "PROXMOX_CA_CERT", "PVE_CA_CERT", "PROXMOX_CA_FILE", "PVE_CA_FILE", "PROXMOX_CA_PATH", "PROXMOX_TOKEN_FILE", "PVE_TOKEN_FILE", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "TELEGRAM_ALERTS_ENABLED", "TELEGRAM_ALERTS_MIN_LEVEL", "KUBECONFIG"} {
		t.Setenv(name, "")
	}
	t.Setenv("PROXMOX_URL", "https://127.0.0.1:1")
	t.Setenv("PROXMOX_API_TOKEN", "migration-provider-token-sentinel")
	t.Setenv("PROXMOX_TOKEN_FILE", filepath.Join(dir, "never-read-provider-token"))
	t.Setenv("ALERTS_CONFIG_FILE", filepath.Join(dir, "alerts.json"))
}

func migrationWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyPreservesDataAndCredentialsWithoutAPI(t *testing.T) {
	for _, source := range []string{"flag", "environment"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			isolateMigrationEnvironment(t, dir)
			ca := newImportCA(t)
			talosCfg := importTalosFixture(t, ca, false)
			talosCfg.Contexts[talosCfg.Context].Endpoints = []string{"127.0.0.1:1"}
			talosCfg.Contexts[talosCfg.Context].Nodes = []string{"127.0.0.2"}
			rawTalos, err := talosCfg.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			talosPath, kubePath := filepath.Join(dir, "talosconfig"), filepath.Join(dir, "kubeconfig")
			migrationWrite(t, talosPath, rawTalos)
			kubeCfg := clientcmdapi.Config{CurrentContext: "production", Contexts: map[string]*clientcmdapi.Context{"production": {Cluster: "production", AuthInfo: "admin"}}, Clusters: map[string]*clientcmdapi.Cluster{"production": {Server: "https://127.0.0.1:1", CertificateAuthorityData: ca.pem}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"admin": {Token: "migration-kube-token-sentinel"}}}
			rawKube, err := clientcmd.Write(kubeCfg)
			if err != nil {
				t.Fatal(err)
			}
			migrationWrite(t, kubePath, rawKube)
			caPath := filepath.Join(dir, "provider-ca.pem")
			migrationWrite(t, caPath, ca.pem)
			t.Setenv("PROXMOX_CA_FILE", caPath)
			alertsPath := filepath.Join(dir, "alerts.json")
			preserved := map[string][]byte{
				alertsPath: []byte(`{"botToken":"migration-telegram-token-sentinel","chatId":"-100001","enabled":true,"minLevel":"WARNING"}`),
				filepath.Join(dir, "data", "jobs", "legacy-job.json"):              []byte(`{"id":"89b2fe1e-1b26-4d0e-aacd-6ebf941b2118","status":"succeeded","request":{"kind":"rolling-reboot"},"user":"admin","events":[]}`),
				filepath.Join(dir, "data", "backups", "legacy-etcd.snapshot"):      []byte("legacy snapshot fixture: preserve exact bytes"),
				filepath.Join(dir, "data", "backups", "legacy-etcd.snapshot.json"): []byte(`{"type":"etcd","size":45}`),
				filepath.Join(dir, "data", "audit.log"):                            []byte("{\"action\":\"backup.create\",\"user\":\"admin\"}\n"),
			}
			for path, data := range preserved {
				migrationWrite(t, path, data)
			}
			dbPath, keyPath := filepath.Join(dir, "data", "clusters.db"), filepath.Join(dir, "keys", "master.key")
			store, err := clusters.Open(dbPath, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			selectedKube := kubePath
			if source == "environment" {
				t.Setenv("KUBECONFIG", kubePath)
				selectedKube = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err = MigrateLegacy(ctx, store, talosPath, selectedKube, []string{"127.0.0.3"}); err != nil {
				t.Fatalf("offline migration with explicit kubeconfig: %v", err)
			}
			list, err := store.List(context.Background())
			if err != nil || len(list) != 1 {
				t.Fatalf("migration registry: %+v %v", list, err)
			}
			cluster := list[0]
			if !cluster.Legacy || cluster.Name != "production" || len(cluster.Endpoints) != 1 || cluster.Endpoints[0] != "127.0.0.1:1" || cluster.Identity == "" {
				t.Fatalf("legacy mapping lost: %+v", cluster)
			}
			credentials, err := store.Credentials(context.Background(), cluster.ID)
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := talosconfig.FromBytes(credentials.Talosconfig)
			if err != nil {
				t.Fatal(err)
			}
			nodes := normalized.Contexts[normalized.Context].Nodes
			if len(nodes) != 2 || nodes[0] != "127.0.0.2" || nodes[1] != "127.0.0.3" {
				t.Fatalf("extra nodes not preserved: %v", nodes)
			}
			migratedKube, err := clientcmd.Load(credentials.Kubeconfig)
			if err != nil {
				t.Fatal(err)
			}
			selected := migratedKube.Contexts[migratedKube.CurrentContext]
			if selected == nil || migratedKube.AuthInfos[selected.AuthInfo].Token != "migration-kube-token-sentinel" {
				t.Fatal("kube credentials lost")
			}
			var providers providerCredentials
			if err = json.Unmarshal(credentials.ProviderSecrets, &providers); err != nil {
				t.Fatal(err)
			}
			if providers.Proxmox.APIToken != "migration-provider-token-sentinel" || providers.Proxmox.CACertFile != "" || providers.Proxmox.CACert != string(ca.pem) {
				t.Fatal("provider secret or embedded CA not preserved")
			}
			if providers.Telegram.BotToken != "migration-telegram-token-sentinel" || providers.Telegram.ChatID != "-100001" || !providers.Telegram.Enabled || providers.Telegram.MinLevel != "WARNING" {
				t.Fatal("legacy alert settings lost")
			}
			onDisk, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"migration-kube-token-sentinel", "migration-provider-token-sentinel", "migration-telegram-token-sentinel", talosCfg.Contexts[talosCfg.Context].Key} {
				if bytes.Contains(onDisk, []byte(secret)) {
					t.Fatal("migration stored plaintext credentials in database")
				}
			}
			for path, want := range preserved {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("migration changed legacy file %s: %v", filepath.Base(path), err)
				}
			}
			// The persistent cluster row is the marker; restarting must not consult old
			// files, lose the old paths, or create another cluster with a fresh UUID.
			for _, path := range []string{talosPath, kubePath, caPath, alertsPath} {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			store.Close()
			store, err = clusters.Open(dbPath, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err = MigrateLegacy(context.Background(), store, talosPath, selectedKube, nil); err != nil {
				t.Fatalf("restart tried to reread removed config: %v", err)
			}
			list, err = store.List(context.Background())
			if err != nil || len(list) != 1 || list[0].ID != cluster.ID || !list[0].Legacy {
				t.Fatalf("duplicate or lost migration: %+v %v", list, err)
			}
		})
	}
}

func TestMigrateLegacyMissingConfigurationStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	isolateMigrationEnvironment(t, dir)
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, path := range []string{"", filepath.Join(dir, "missing-talosconfig")} {
		if err = MigrateLegacy(context.Background(), store, path, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("missing legacy created phantom cluster: %+v %v", list, err)
	}
}

func TestMigrateLegacyInvalidProviderCAFailsWithoutPartialImport(t *testing.T) {
	dir := t.TempDir()
	isolateMigrationEnvironment(t, dir)
	ca := newImportCA(t)
	cfg := importTalosFixture(t, ca, false)
	cfg.Contexts[cfg.Context].Endpoints = []string{"127.0.0.1:1"}
	raw, err := cfg.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	talosPath := filepath.Join(dir, "talosconfig")
	migrationWrite(t, talosPath, raw)
	kubeCfg := clientcmdapi.Config{CurrentContext: "test", Contexts: map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}}, Clusters: map[string]*clientcmdapi.Cluster{"test": {Server: "https://127.0.0.1:1", CertificateAuthorityData: ca.pem}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test-token"}}}
	raw, err = clientcmd.Write(kubeCfg)
	if err != nil {
		t.Fatal(err)
	}
	kubePath := filepath.Join(dir, "kubeconfig")
	migrationWrite(t, kubePath, raw)
	t.Setenv("PROXMOX_CA_FILE", filepath.Join(dir, "missing-ca.pem"))
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("migration panicked instead of reporting invalid provider CA: %v", p)
		}
	}()
	if err = MigrateLegacy(context.Background(), store, talosPath, kubePath, nil); err == nil {
		t.Fatal("invalid provider CA silently discarded")
	}
	list, err := store.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("failed migration left partial registry: %+v %v", list, err)
	}
}
