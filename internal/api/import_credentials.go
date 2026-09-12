package api

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
)

// Only retain the explicitly selected context, never other fleet credentials.
func normalizeTalosConfig(data []byte, extraNodes []string) ([]byte, string, string, []string, error) {
	cfg, err := talosconfig.FromBytes(data)
	if err != nil || cfg == nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: invalid YAML")
	}
	c := cfg.Contexts[cfg.Context]
	if c == nil || cfg.Context == "" || len(c.Endpoints) == 0 {
		return nil, "", "", nil, fmt.Errorf("talosconfig: select a context with endpoints")
	}
	if c.ProxyURL != "" || c.Auth.Basic != nil || c.Auth.SideroV1 != nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: direct mTLS credentials required")
	}
	addresses := append(append(append([]string{}, c.Endpoints...), c.Nodes...), extraNodes...)
	for _, endpoint := range addresses {
		if strings.ContainsAny(endpoint, "/\\?#@ \t\r\n") || strings.HasPrefix(endpoint, "-") || endpoint == "" {
			return nil, "", "", nil, fmt.Errorf("talosconfig: invalid endpoint or node address")
		}
		if strings.Contains(endpoint, ":") && net.ParseIP(endpoint) == nil {
			host, port, err := net.SplitHostPort(endpoint)
			p, pe := strconv.Atoi(port)
			if err != nil || host == "" || pe != nil || p < 1 || p > 65535 {
				return nil, "", "", nil, fmt.Errorf("talosconfig: invalid endpoint address")
			}
		}
	}
	caPEM, err := base64.StdEncoding.DecodeString(c.CA)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: invalid CA data")
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: missing CA certificate")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		return nil, "", "", nil, fmt.Errorf("talosconfig: invalid CA certificate")
	}
	crt, e1 := base64.StdEncoding.DecodeString(c.Crt)
	key, e2 := base64.StdEncoding.DecodeString(c.Key)
	pair, e3 := tls.X509KeyPair(crt, key)
	if e1 != nil || e2 != nil || e3 != nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: client certificate and private key do not match")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().After(cert.NotAfter) || time.Now().Before(cert.NotBefore) {
		return nil, "", "", nil, fmt.Errorf("talosconfig: client certificate is expired or not yet valid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	intermediates := x509.NewCertPool()
	for _, raw := range pair.Certificate[1:] {
		intermediate, e := x509.ParseCertificate(raw)
		if e != nil {
			return nil, "", "", nil, fmt.Errorf("talosconfig: invalid client certificate chain")
		}
		intermediates.AddCert(intermediate)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: client certificate is not valid for this CA")
	}
	c.Nodes = append(c.Nodes, extraNodes...)
	cfg.Contexts = map[string]*talosconfig.Context{cfg.Context: c}
	normalized, err := cfg.Bytes()
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("talosconfig: could not normalize configuration")
	}
	fingerprint := sha256.Sum256(ca.Raw)
	return normalized, hex.EncodeToString(fingerprint[:]), cfg.Context, c.Endpoints, nil
}
