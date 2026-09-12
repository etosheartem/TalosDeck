package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
)

type importCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newImportCA(t *testing.T) importCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Talos test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return importCA{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func importTalosFixture(t *testing.T, ca importCA, expired bool) *talosconfig.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().Add(time.Hour)
	if expired {
		notAfter = time.Now().Add(-time.Minute)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Talos test user", Organization: []string{"os:admin"}}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString
	return &talosconfig.Config{Context: "production", Contexts: map[string]*talosconfig.Context{"production": {Endpoints: []string{"10.0.0.1"}, Nodes: []string{"10.0.0.2"}, CA: enc(ca.pem), Crt: enc(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Key: enc(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}))}}}
}

func TestNormalizeTalosConfigIdentitySurvivesClientRotation(t *testing.T) {
	ca := newImportCA(t)
	first := importTalosFixture(t, ca, false)
	other := importTalosFixture(t, newImportCA(t), false)
	first.Contexts["unselected"] = other.Contexts["production"]
	raw, err := first.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	normalized, id, name, endpoints, err := normalizeTalosConfig(raw, []string{"10.0.0.3"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "production" || len(endpoints) != 1 || id == "" {
		t.Fatalf("identity metadata: %s %s %v", id, name, endpoints)
	}
	cfg, err := talosconfig.FromBytes(normalized)
	if err != nil || len(cfg.Contexts) != 1 || len(cfg.Contexts["production"].Nodes) != 2 {
		t.Fatalf("context normalization: %v", err)
	}
	if bytes.Contains(normalized, []byte(other.Contexts["production"].Key)) {
		t.Fatal("unselected private key retained")
	}
	rotated, _ := importTalosFixture(t, ca, false).Bytes()
	_, rotatedID, _, _, err := normalizeTalosConfig(rotated, nil)
	if err != nil || rotatedID != id {
		t.Fatalf("client rotation changed cluster identity: %v", err)
	}
	otherRaw, _ := other.Bytes()
	_, otherID, _, _, err := normalizeTalosConfig(otherRaw, nil)
	if err != nil || otherID == id {
		t.Fatalf("independent cluster identity: %v", err)
	}
}

func TestNormalizeTalosConfigRejectsInvalidCredentials(t *testing.T) {
	ca := newImportCA(t)
	cases := map[string]func(*talosconfig.Config){
		"missing-context":  func(c *talosconfig.Config) { c.Context = "missing" },
		"empty-endpoints":  func(c *talosconfig.Config) { c.Contexts[c.Context].Endpoints = nil },
		"option-injection": func(c *talosconfig.Config) { c.Contexts[c.Context].Endpoints = []string{"--insecure"} },
		"url":              func(c *talosconfig.Config) { c.Contexts[c.Context].Endpoints = []string{"https://cluster.example"} },
		"empty-host":       func(c *talosconfig.Config) { c.Contexts[c.Context].Endpoints = []string{":50000"} },
		"bad-port":         func(c *talosconfig.Config) { c.Contexts[c.Context].Endpoints = []string{"cluster.example:99999"} },
		"empty-port":       func(c *talosconfig.Config) { c.Contexts[c.Context].Endpoints = []string{"cluster.example:"} },
		"proxy":            func(c *talosconfig.Config) { c.Contexts[c.Context].ProxyURL = "https://proxy.example" },
		"ca-data":          func(c *talosconfig.Config) { c.Contexts[c.Context].CA = "bad-base64" },
		"mismatched-key": func(c *talosconfig.Config) {
			c.Contexts[c.Context].Key = importTalosFixture(t, ca, false).Contexts["production"].Key
		},
		"wrong-ca": func(c *talosconfig.Config) {
			c.Contexts[c.Context].CA = base64.StdEncoding.EncodeToString(newImportCA(t).pem)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := importTalosFixture(t, ca, false)
			secret := cfg.Contexts[cfg.Context].Key
			mutate(cfg)
			raw, err := cfg.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, _, err = normalizeTalosConfig(raw, nil)
			if err == nil {
				t.Fatal("invalid config accepted")
			}
			if bytes.Contains([]byte(err.Error()), []byte(secret)) {
				t.Fatal("error leaked key")
			}
		})
	}
	expired, _ := importTalosFixture(t, ca, true).Bytes()
	if _, _, _, _, err := normalizeTalosConfig(expired, nil); err == nil {
		t.Fatal("expired certificate accepted")
	}
	valid, _ := importTalosFixture(t, ca, false).Bytes()
	if _, _, _, _, err := normalizeTalosConfig(valid, []string{"--insecure"}); err == nil {
		t.Fatal("extra node bypassed validation")
	}
}
