package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/google/uuid"
	"talosdeck/internal/alerts"
	"talosdeck/internal/clusters"
	"talosdeck/internal/k8s"
	"talosdeck/internal/proxmox"
	"talosdeck/internal/talos"
)

// MigrateLegacy keeps existing journals/backups at their original paths. The
// atomic cluster insert is the migration marker, so restarts cannot import twice.
func MigrateLegacy(ctx context.Context, store *clusters.Store, talosPath, kubePath string, extraNodes []string) error {
	list, err := store.List(ctx)
	if err != nil {
		return err
	}
	if len(list) > 0 || talosPath == "" {
		return nil
	}
	raw, err := os.ReadFile(talosPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot read legacy talosconfig")
	}
	raw, identity, name, endpoints, err := normalizeTalosConfig(raw, extraNodes)
	if err != nil {
		return err
	}
	tm, err := talos.NewTalosManager(talosPath, extraNodes...)
	if err != nil {
		return errors.New("cannot initialize legacy Talos client")
	}
	defer tm.Close()
	var kube []byte
	// Prefer durable Talos-issued credentials over expiring in-cluster tokens.
	if kubePath == "" && os.Getenv("KUBECONFIG") == "" {
		kube, err = tm.GetClient().Kubeconfig(ctx)
		if err == nil {
			kube, err = k8s.NormalizeConfig(kube)
		}
		if err != nil {
			return errors.New("cannot obtain durable kubeconfig for migration; provide --kubeconfig with embedded credentials")
		}
	} else {
		km, err := k8s.NewK8sManager(kubePath, tm.GetClient().Kubeconfig)
		if err != nil {
			return errors.New("cannot load legacy kubeconfig")
		}
		kube, err = km.PortableConfig()
		if err != nil {
			return err
		}
	}
	providerClient := proxmox.NewClientFromEnv()
	if providerClient == nil {
		return errors.New("cannot initialize legacy provider; check provider CA configuration")
	}
	providers := providerCredentials{Proxmox: providerClient.PrivateConfig(), Telegram: alerts.NewTelegramServiceFromEnv().PrivateConfig()}
	if providers.Proxmox.CACertFile != "" {
		ca, err := os.ReadFile(providers.Proxmox.CACertFile)
		if err != nil {
			return errors.New("cannot read legacy provider CA")
		}
		providers.Proxmox.CACert = string(ca)
		providers.Proxmox.CACertFile = ""
	}
	secrets, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	_, err = store.Create(ctx, clusters.Cluster{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String(), Name: name, Endpoints: endpoints, Identity: identity, Health: "unknown", Legacy: true}, clusters.Credentials{Talosconfig: raw, Kubeconfig: kube, ProviderSecrets: secrets})
	return err
}
