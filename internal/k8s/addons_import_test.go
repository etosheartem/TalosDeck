package k8s

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func TestImportedManagerRetainsTLSCredentialsForAddonDiscovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer selected-secret" {
			t.Error("addon discovery lost imported authentication")
		}
		requests.Add(1)
		http.Error(w, "discovery not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := importFixture(t)
	cfg.Clusters["selected"].Server = server.URL
	cfg.Clusters["selected"].CertificateAuthorityData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	raw, err := clientcmd.Write(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewK8sManagerFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.InstallAddon(context.Background(), "local-path"); err == nil {
		t.Fatal("unavailable discovery accepted")
	}
	if requests.Load() == 0 {
		t.Fatal("addon did not reach the imported TLS API")
	}
}

func TestAddonMissingCredentialsFailWithoutPanic(t *testing.T) {
	for _, manager := range []*K8sManager{nil, {}} {
		if err := manager.InstallAddon(context.Background(), "cilium"); err == nil {
			t.Fatal("missing addon credentials accepted")
		}
		if manager.AddonReady(context.Background(), "cilium") {
			t.Fatal("missing client reported addon ready")
		}
	}
}
