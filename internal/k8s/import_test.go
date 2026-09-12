package k8s

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func importFixture(t *testing.T) *clientcmdapi.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &clientcmdapi.Config{CurrentContext: "selected", Contexts: map[string]*clientcmdapi.Context{"selected": {Cluster: "selected", AuthInfo: "selected"}}, Clusters: map[string]*clientcmdapi.Cluster{"selected": {Server: "https://cluster.example:6443", CertificateAuthorityData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"selected": {Token: "selected-secret"}}}
}

func TestNormalizeConfigRejectsUnsafeSelectedCredentials(t *testing.T) {
	cases := map[string]func(*clientcmdapi.Config){
		"exec": func(c *clientcmdapi.Config) {
			c.AuthInfos["selected"].Exec = &clientcmdapi.ExecConfig{Command: "/bin/sh", Args: []string{"-c", "echo secret"}}
		},
		"auth-provider": func(c *clientcmdapi.Config) {
			c.AuthInfos["selected"].AuthProvider = &clientcmdapi.AuthProviderConfig{Name: "oidc"}
		},
		"token-file":        func(c *clientcmdapi.Config) { c.AuthInfos["selected"].TokenFile = "/etc/passwd" },
		"certificate-file":  func(c *clientcmdapi.Config) { c.AuthInfos["selected"].ClientCertificate = "/etc/passwd" },
		"key-file":          func(c *clientcmdapi.Config) { c.AuthInfos["selected"].ClientKey = "/etc/passwd" },
		"ca-file":           func(c *clientcmdapi.Config) { c.Clusters["selected"].CertificateAuthority = "/etc/passwd" },
		"insecure":          func(c *clientcmdapi.Config) { c.Clusters["selected"].InsecureSkipTLSVerify = true },
		"proxy":             func(c *clientcmdapi.Config) { c.Clusters["selected"].ProxyURL = "http://proxy.example" },
		"http":              func(c *clientcmdapi.Config) { c.Clusters["selected"].Server = "http://cluster.example" },
		"url-password":      func(c *clientcmdapi.Config) { c.Clusters["selected"].Server = "https://user:secret@cluster.example" },
		"url-query":         func(c *clientcmdapi.Config) { c.Clusters["selected"].Server = "https://cluster.example?token=secret" },
		"empty-host":        func(c *clientcmdapi.Config) { c.Clusters["selected"].Server = "https://:6443" },
		"bad-port":          func(c *clientcmdapi.Config) { c.Clusters["selected"].Server = "https://cluster.example:65536" },
		"invalid-ca":        func(c *clientcmdapi.Config) { c.Clusters["selected"].CertificateAuthorityData = []byte("secret") },
		"impersonate":       func(c *clientcmdapi.Config) { c.AuthInfos["selected"].Impersonate = "admin" },
		"impersonate-uid":   func(c *clientcmdapi.Config) { c.AuthInfos["selected"].ImpersonateUID = "admin" },
		"impersonate-group": func(c *clientcmdapi.Config) { c.AuthInfos["selected"].ImpersonateGroups = []string{"admin"} },
		"impersonate-extra": func(c *clientcmdapi.Config) {
			c.AuthInfos["selected"].ImpersonateUserExtra = map[string][]string{"scope": {"admin"}}
		},
		"basic-password": func(c *clientcmdapi.Config) { c.AuthInfos["selected"].Password = "secret" },
		"missing-user":   func(c *clientcmdapi.Config) { c.Contexts["selected"].AuthInfo = "" },
		"empty-auth":     func(c *clientcmdapi.Config) { c.AuthInfos["selected"].Token = "" },
		"invalid-client-cert": func(c *clientcmdapi.Config) {
			c.AuthInfos["selected"].ClientCertificateData = []byte("secret")
			c.AuthInfos["selected"].ClientKeyData = []byte("secret")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := importFixture(t)
			mutate(cfg)
			raw, err := clientcmd.Write(*cfg)
			if err != nil {
				t.Fatal(err)
			}
			_, err = NormalizeConfig(raw)
			if err == nil {
				t.Fatal("unsafe config accepted")
			}
			if bytes.Contains([]byte(err.Error()), []byte("selected-secret")) {
				t.Fatal("credentials leaked in error")
			}
		})
	}
}

func TestNormalizeConfigDropsOtherContextsAndNeverFallsBack(t *testing.T) {
	cfg := importFixture(t)
	cfg.Clusters["other"] = &clientcmdapi.Cluster{Server: "https://other.example", CertificateAuthority: "/etc/passwd"}
	cfg.AuthInfos["other"] = &clientcmdapi.AuthInfo{Token: "unselected-secret", Exec: &clientcmdapi.ExecConfig{Command: "touch"}}
	cfg.Contexts["other"] = &clientcmdapi.Context{Cluster: "other", AuthInfo: "other"}
	raw, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(normalized, []byte("unselected-secret")) || bytes.Contains(normalized, []byte("/etc/passwd")) {
		t.Fatal("unselected credentials retained")
	}
	selected, err := clientcmd.Load(normalized)
	if err != nil || len(selected.Clusters) != 1 || len(selected.AuthInfos) != 1 {
		t.Fatalf("selected context: %v", err)
	}
	if _, err = NewK8sManagerFromBytes(normalized); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", "/etc/passwd")
	t.Setenv("KUBERNETES_SERVICE_HOST", "127.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	for _, invalid := range [][]byte{nil, []byte("not: a kubeconfig"), []byte("current-context: missing")} {
		if _, err = NewK8sManagerFromBytes(invalid); err == nil {
			t.Fatal("invalid explicit config fell back to environment")
		}
	}
}
