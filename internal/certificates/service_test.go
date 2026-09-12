package certificates

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"
)

type fixturePKI struct {
	ca  *x509.Certificate
	key *ecdsa.PrivateKey
	pem []byte
	now time.Time
}

func pki(t *testing.T) fixturePKI {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := x509.ParseCertificate(der)
	return fixturePKI{parsed, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), now}
}
func (p fixturePKI) certificate(t *testing.T, before, after time.Time) ([]byte, []byte, tls.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(after.UnixNano()), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: before, NotAfter: after, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	keyder, _ := x509.MarshalECPrivateKey(key)
	certpem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keypem := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyder})
	pair, err := tls.X509KeyPair(certpem, keypem)
	if err != nil {
		t.Fatal(err)
	}
	return certpem, keypem, pair
}
func server(t *testing.T, pair tls.Certificate) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{pair}}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}
func configs(t *testing.T, p fixturePKI, endpoint string, client, key []byte) ([]byte, []byte) {
	t.Helper()
	tc := &talosconfig.Config{Context: "selected", Contexts: map[string]*talosconfig.Context{"selected": {CA: base64.StdEncoding.EncodeToString(p.pem), Crt: base64.StdEncoding.EncodeToString(client), Key: base64.StdEncoding.EncodeToString(key), Endpoints: []string{strings.TrimPrefix(endpoint, "https://")}}, "ignored": {CA: "SECRET-UNSELECTED", Endpoints: []string{"do-not-contact.invalid"}}}}
	tb, err := tc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	kc := clientapi.NewConfig()
	kc.CurrentContext = "selected"
	kc.Contexts["selected"] = &clientapi.Context{Cluster: "cluster", AuthInfo: "user"}
	kc.Clusters["cluster"] = &clientapi.Cluster{Server: endpoint, CertificateAuthorityData: p.pem}
	kc.AuthInfos["user"] = &clientapi.AuthInfo{ClientCertificateData: client, ClientKeyData: key}
	kb, err := clientcmd.Write(*kc)
	if err != nil {
		t.Fatal(err)
	}
	return tb, kb
}
func TestLifetimeThresholds(t *testing.T) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		duration       time.Duration
		status, reason string
	}{{31 * 24 * time.Hour, "healthy", "valid"}, {30 * 24 * time.Hour, "warning", "expires-within-30-days"}, {14 * 24 * time.Hour, "warning", "expires-within-14-days"}, {7 * 24 * time.Hour, "critical", "expires-within-7-days"}, {0, "critical", "expired"}, {-time.Hour, "critical", "expired"}} {
		t.Run(tt.reason+tt.duration.String(), func(t *testing.T) {
			got := lifetime(Certificate{}, &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(tt.duration)}, now)
			if got.Status != tt.status || got.Reason != tt.reason {
				t.Fatalf("wrong classification %+v", got)
			}
		})
	}
	got := lifetime(Certificate{}, &x509.Certificate{NotBefore: now.Add(time.Hour), NotAfter: now.Add(365 * 24 * time.Hour)}, now)
	if got.Reason != "not-yet-valid" || got.Status != "critical" {
		t.Fatal(got)
	}
}
func TestSelectedContextsAndVerifiedServerCertificates(t *testing.T) {
	p := pki(t)
	client, key, pair := p.certificate(t, p.now.Add(-time.Hour), p.now.Add(60*24*time.Hour))
	srv := server(t, pair)
	tc, kc := configs(t, p, srv.URL, client, key)
	s := Service{Talosconfig: tc, Kubeconfig: kc, NodeAddresses: func(context.Context) ([]string, error) { return []string{strings.TrimPrefix(srv.URL, "https://")}, nil }}
	report := s.Check(context.Background())
	if report.Status != "healthy" || len(report.Certificates) != 6 {
		t.Fatalf("wrong report %+v", report)
	}
	verified := 0
	for _, item := range report.Certificates {
		if item.Verified {
			verified++
			if item.Verification != "verified" {
				t.Fatal(item)
			}
		}
	}
	if verified != 2 {
		t.Fatalf("expected both peer chains verified, got%d", verified)
	}
	raw, _ := json.Marshal(report)
	for _, secret := range []string{"BEGIN CERTIFICATE", "PRIVATE KEY", "SECRET-UNSELECTED", base64.StdEncoding.EncodeToString(key)} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("sensitive credential material leaked")
		}
	}
}
func TestFailedTLSStillReportsObservedExpiredLeaf(t *testing.T) {
	p := pki(t)
	client, key, _ := p.certificate(t, p.now.Add(-time.Hour), p.now.Add(60*24*time.Hour))
	_, _, expired := p.certificate(t, p.now.Add(-48*time.Hour), p.now.Add(-time.Hour))
	srv := server(t, expired)
	tc, kc := configs(t, p, srv.URL, client, key)
	report := (&Service{Talosconfig: tc, Kubeconfig: kc}).Check(context.Background())
	count := 0
	for _, item := range report.Certificates {
		if item.Source == "talos-server" || item.Source == "kubernetes-server" {
			count++
			if item.Verified || item.Verification != "failed" || item.Status != "critical" || item.Reason != "expired" || item.NotAfter == nil {
				t.Fatalf("untrusted expired observation lost or trusted %+v", item)
			}
		}
	}
	if count != 2 {
		t.Fatal("missing endpoint observations")
	}
}
func TestUnknownIssuerNeverReportsVerifiedHealthy(t *testing.T) {
	p := pki(t)
	other := pki(t)
	client, key, _ := p.certificate(t, p.now.Add(-time.Hour), p.now.Add(60*24*time.Hour))
	_, _, pair := other.certificate(t, p.now.Add(-time.Hour), p.now.Add(60*24*time.Hour))
	srv := server(t, pair)
	tc, kc := configs(t, p, srv.URL, client, key)
	report := (&Service{Talosconfig: tc, Kubeconfig: kc}).Check(context.Background())
	for _, item := range report.Certificates {
		if item.Source == "talos-server" || item.Source == "kubernetes-server" {
			if item.Verified || item.Verification != "failed" || item.Status != "unknown" {
				t.Fatalf("untrusted peer treated healthy %+v", item)
			}
		}
	}
}
func TestTokenExpiryUnknownAndDiscoveryFallback(t *testing.T) {
	p := pki(t)
	client, key, pair := p.certificate(t, p.now.Add(-time.Hour), p.now.Add(60*24*time.Hour))
	srv := server(t, pair)
	tc, kc := configs(t, p, srv.URL, client, key)
	cfg, _ := clientcmd.Load(kc)
	cfg.AuthInfos["user"] = &clientapi.AuthInfo{Token: "eyJhbGciOiJub25lIn0.eyJleHAiOjQ3NDAwMDAwMDB9."}
	kc, _ = clientcmd.Write(*cfg)
	report := (&Service{Talosconfig: tc, Kubeconfig: kc, NodeAddresses: func(context.Context) ([]string, error) { return nil, errors.New("do not expose secret-raw-error") }}).Check(context.Background())
	found := map[string]Certificate{}
	for _, item := range report.Certificates {
		found[item.ID] = item
	}
	if found["kube-token"].Status != "unknown" || found["kube-token"].NotAfter != nil || found["talos-discovery"].Status != "unknown" {
		t.Fatal(report)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(string(raw), "eyJ") || strings.Contains(string(raw), "secret-raw-error") {
		t.Fatal("raw secret leaked")
	}
	if !found["kube-server"].Verified {
		t.Fatal("token-based API peer not checked")
	}
}
func TestCancellationBoundsStalledTLS(t *testing.T) {
	p := pki(t)
	client, key, _ := p.certificate(t, p.now.Add(-time.Hour), p.now.Add(60*24*time.Hour))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	conns := []net.Conn{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	defer func() {
		listener.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	}()
	tc, kc := configs(t, p, "https://"+listener.Addr().String(), client, key)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	report := (&Service{Talosconfig: tc, Kubeconfig: kc, Timeout: time.Second, Parallelism: 1}).Check(ctx)
	if time.Since(start) > time.Second {
		t.Fatal("check ignored caller deadline")
	}
	if report.Summary.Unknown != 2 {
		t.Fatalf("stalled peers not unknown %+v", report)
	}
}
func TestLiveReadonlyCertificateInspection(t *testing.T) {
	tp, kp := os.Getenv("TALOSDECK_TEST_CERT_TALOSCONFIG"), os.Getenv("TALOSDECK_TEST_CERT_KUBECONFIG")
	if tp == "" || kp == "" {
		t.Skip("explicit readonly live certificate configs required")
	}
	tc, err := os.ReadFile(tp)
	if err != nil {
		t.Fatal(err)
	}
	kc, err := os.ReadFile(kp)
	if err != nil {
		t.Fatal(err)
	}
	nodes := strings.FieldsFunc(os.Getenv("TALOSDECK_TEST_CERT_NODES"), func(r rune) bool { return r == ',' })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	report := (&Service{Talosconfig: tc, Kubeconfig: kc, NodeAddresses: func(context.Context) ([]string, error) { return nodes, nil }}).Check(ctx)
	for _, item := range report.Certificates {
		t.Logf("%s %s %s verified=%t reason=%s", item.Source, item.Endpoint, item.Status, item.Verified, item.Reason)
	}
	if out := os.Getenv("TALOSDECK_TEST_CERT_REPORT"); out != "" {
		raw, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(out, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	peers := 0
	for _, item := range report.Certificates {
		if item.Verified {
			peers++
		}
	}
	if peers < len(nodes)+1 {
		t.Fatalf("expected node and Kubernetes verified peers, got%d", peers)
	}
}

func TestTalosAutomaticallyRotatedServerPolicy(t *testing.T) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		remaining      time.Duration
		status, reason string
	}{
		{48 * time.Hour, "healthy", "valid"},
		{time.Hour, "warning", "expires-within-1-hour"},
		{10 * time.Minute, "critical", "expires-within-10-minutes"},
		{0, "critical", "expired"},
	} {
		got := lifetime(Certificate{Source: "talos-server"}, &x509.Certificate{NotBefore: now.Add(-time.Hour), NotAfter: now.Add(tt.remaining)}, now)
		if got.Status != tt.status || got.Reason != tt.reason {
			t.Fatalf("wrong rotating server policy %+v", got)
		}
	}
}
