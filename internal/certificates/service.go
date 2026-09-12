// Package certificates observes credential and endpoint certificate lifetimes.
// It never renews credentials or returns certificate/key/token material.
package certificates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	talosconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"k8s.io/client-go/tools/clientcmd"
)

type Certificate struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Source          string     `json:"source"`
	Endpoint        string     `json:"endpoint,omitempty"`
	Node            string     `json:"node,omitempty"`
	NotBefore       *time.Time `json:"notBefore,omitempty"`
	NotAfter        *time.Time `json:"notAfter,omitempty"`
	DaysRemaining   *int       `json:"daysRemaining,omitempty"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason"`
	Verified        bool       `json:"verified"`
	Verification    string     `json:"verification"`
	Error           string     `json:"error,omitempty"`
	RenewalGuidance string     `json:"renewalGuidance"`
}
type Summary struct {
	Healthy  int `json:"healthy"`
	Warning  int `json:"warning"`
	Critical int `json:"critical"`
	Unknown  int `json:"unknown"`
}
type Report struct {
	CheckedAt    time.Time     `json:"checkedAt"`
	Certificates []Certificate `json:"certificates"`
	Summary      Summary       `json:"summary"`
	Status       string        `json:"status"`
}
type Service struct {
	Talosconfig   []byte
	Kubeconfig    []byte
	NodeAddresses func(context.Context) ([]string, error)
	Timeout       time.Duration
	Parallelism   int
	// Now supports deterministic observation/testing; defaults to time.Now.
	Now func() time.Time
}
type target struct {
	item   Certificate
	config *tls.Config
}

func unknown(id, name, source, message, guidance string) Certificate {
	return Certificate{ID: id, Name: name, Source: source, Status: "unknown", Reason: "unavailable", Verification: "unavailable", Error: message, RenewalGuidance: guidance}
}

const talosGuidance = "Issue replacement Talos client credentials outside TalosDeck with the cluster owner before expiry. This monitor cannot replace stored credentials; consult the deployment documentation."
const kubeGuidance = "Obtain replacement kubeconfig credentials outside TalosDeck from the Kubernetes cluster owner. This monitor cannot replace stored credentials; consult the deployment documentation."
const talosCAGuidance = "Plan Talos CA rotation with the cluster owner using the supported Talos procedure. Renewing a client certificate does not renew its CA; this monitor cannot rotate CAs or replace stored credentials."
const kubeCAGuidance = "Plan Kubernetes CA rotation with the cluster owner using the supported Talos procedure. Reissuing kubeconfig credentials does not renew the cluster CA; this monitor cannot rotate CAs or replace stored credentials."

const talosServerGuidance = "Talos API server certificates are automatically rotated and short-lived. The 1-hour warning and 10-minute critical thresholds are TalosDeck monitoring policy. Check node time and rotation health; do not manually replace a healthy rotating certificate or disable TLS verification."

const serverGuidance = "Inspect the node time, server certificate chain and SANs; renew through the cluster's Talos/Kubernetes certificate lifecycle. Do not disable TLS verification."

func lifetime(item Certificate, cert *x509.Certificate, now time.Time) Certificate {
	before, after := cert.NotBefore.UTC(), cert.NotAfter.UTC()
	days := int(math.Floor(after.Sub(now).Hours() / 24))
	item.NotBefore = &before
	item.NotAfter = &after
	item.DaysRemaining = &days
	remaining := after.Sub(now)
	switch {
	case now.Before(before):
		item.Status = "critical"
		item.Reason = "not-yet-valid"
	case !now.Before(after):
		item.Status = "critical"
		item.Reason = "expired"
	case item.Source == "talos-server" && remaining <= 10*time.Minute:
		item.Status = "critical"
		item.Reason = "expires-within-10-minutes"
	case item.Source == "talos-server" && remaining <= time.Hour:
		item.Status = "warning"
		item.Reason = "expires-within-1-hour"
	case item.Source == "talos-server":
		item.Status = "healthy"
		item.Reason = "valid"
	case remaining <= 7*24*time.Hour:
		item.Status = "critical"
		item.Reason = "expires-within-7-days"
	case remaining <= 14*24*time.Hour:
		item.Status = "warning"
		item.Reason = "expires-within-14-days"
	case remaining <= 30*24*time.Hour:
		item.Status = "warning"
		item.Reason = "expires-within-30-days"
	default:
		item.Status = "healthy"
		item.Reason = "valid"
	}
	return item
}
func embedded(id, name, source string, data []byte, now time.Time, guidance string) []Certificate {
	items := []Certificate{}
	index := 0
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		index++
		item := Certificate{ID: fmt.Sprintf("%s-%d", id, index), Name: name, Source: source, Verification: "embedded", RenewalGuidance: guidance}
		if index > 1 {
			item.Name = fmt.Sprintf("%s #%d", name, index)
		}
		items = append(items, lifetime(item, cert, now))
	}
	if len(items) == 0 {
		return []Certificate{unknown(id, name, source, "Embedded certificate unavailable or malformed", guidance)}
	}
	return items
}
func address(raw, defaultPort string) (string, string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "/?#@\\\r\n\t") {
		return "", "", errors.New("invalid endpoint")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		host = strings.Trim(raw, "[]")
		port = defaultPort
		if strings.Contains(host, ":") && net.ParseIP(host) == nil {
			return "", "", errors.New("invalid endpoint")
		}
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 || host == "" {
		return "", "", errors.New("invalid endpoint")
	}
	return net.JoinHostPort(host, port), host, nil
}
func trustConfig(ca, crt, key []byte) (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("trusted CA unavailable")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if len(crt) > 0 || len(key) > 0 {
		pair, err := tls.X509KeyPair(crt, key)
		if err != nil {
			return nil, errors.New("embedded client certificate/key unavailable")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}
func (s *Service) Check(ctx context.Context) Report {
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	report := Report{CheckedAt: now, Certificates: []Certificate{}, Status: "healthy"}
	targets := []target{}
	cfg, err := talosconfig.FromBytes(s.Talosconfig)
	if err != nil || cfg == nil || cfg.Context == "" || cfg.Contexts[cfg.Context] == nil {
		report.Certificates = append(report.Certificates, unknown("talos-config", "Talos credentials", "talosconfig", "Selected Talos context unavailable", talosGuidance))
	} else {
		selected := cfg.Contexts[cfg.Context]
		ca, _ := base64.StdEncoding.DecodeString(selected.CA)
		crt, _ := base64.StdEncoding.DecodeString(selected.Crt)
		key, _ := base64.StdEncoding.DecodeString(selected.Key)
		report.Certificates = append(report.Certificates, embedded("talos-ca", "Talos CA", "talosconfig", ca, now, talosCAGuidance)...)
		report.Certificates = append(report.Certificates, embedded("talos-client", "Talos client certificate", "talosconfig", crt, now, talosGuidance)...)
		tlsConfig, tlsErr := trustConfig(ca, crt, key)
		if selected.ProxyURL != "" || selected.Auth.Basic != nil || selected.Auth.SideroV1 != nil {
			tlsErr = errors.New("unsupported Talos proxy or external authentication")
		}
		addresses := append(append([]string{}, selected.Endpoints...), selected.Nodes...)
		if s.NodeAddresses != nil {
			nodes, e := s.NodeAddresses(ctx)
			if e != nil {
				report.Certificates = append(report.Certificates, unknown("talos-discovery", "Talos node discovery", "talos-server", "Node discovery unavailable; configured endpoints are still checked", serverGuidance))
			} else {
				addresses = append(addresses, nodes...)
			}
		}
		seen := map[string]bool{}
		for _, raw := range addresses {
			endpoint, host, e := address(raw, "50000")
			if e != nil {
				report.Certificates = append(report.Certificates, unknown(fmt.Sprintf("talos-invalid-%d", len(seen)), "Talos endpoint", "talos-server", "Invalid configured endpoint", serverGuidance))
				continue
			}
			if seen[endpoint] {
				continue
			}
			seen[endpoint] = true
			item := unknown("talos-server:"+endpoint, "Talos API certificate", "talos-server", "TLS credentials unavailable", talosServerGuidance)
			item.Endpoint = endpoint
			item.Node = host
			if tlsErr != nil {
				report.Certificates = append(report.Certificates, item)
				continue
			}
			c := tlsConfig.Clone()
			c.ServerName = host
			targets = append(targets, target{item, c})
		}
		if len(addresses) == 0 {
			report.Certificates = append(report.Certificates, unknown("talos-endpoints", "Talos endpoints", "talos-server", "No node endpoints available", serverGuidance))
		}
	}
	kube, e := clientcmd.Load(s.Kubeconfig)
	if e != nil || kube == nil || kube.CurrentContext == "" || kube.Contexts[kube.CurrentContext] == nil {
		report.Certificates = append(report.Certificates, unknown("kube-config", "Kubernetes credentials", "kubeconfig", "Selected Kubernetes context unavailable", kubeGuidance))
	} else {
		selected := kube.Contexts[kube.CurrentContext]
		cluster, user := kube.Clusters[selected.Cluster], kube.AuthInfos[selected.AuthInfo]
		if cluster == nil || user == nil {
			report.Certificates = append(report.Certificates, unknown("kube-config", "Kubernetes credentials", "kubeconfig", "Selected Kubernetes cluster/user unavailable", kubeGuidance))
		} else {
			report.Certificates = append(report.Certificates, embedded("kube-ca", "Kubernetes CA", "kubeconfig", cluster.CertificateAuthorityData, now, kubeCAGuidance)...)
			if len(user.ClientCertificateData) > 0 {
				report.Certificates = append(report.Certificates, embedded("kube-client", "Kubernetes client certificate", "kubeconfig", user.ClientCertificateData, now, kubeGuidance)...)
			} else {
				report.Certificates = append(report.Certificates, unknown("kube-token", "Kubernetes credential expiry", "kubeconfig", "Token/external credential expiry is unknown; unsigned token claims are not trusted", kubeGuidance))
			}
			item := unknown("kube-server", "Kubernetes API certificate", "kubernetes-server", "TLS endpoint or embedded credentials unavailable", serverGuidance)
			u, urlErr := url.Parse(cluster.Server)
			tlsConfig, tlsErr := trustConfig(cluster.CertificateAuthorityData, user.ClientCertificateData, user.ClientKeyData)
			safe := urlErr == nil && u.Scheme == "https" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Host != "" && !cluster.InsecureSkipTLSVerify && cluster.ProxyURL == "" && cluster.CertificateAuthority == "" && user.Exec == nil && user.AuthProvider == nil && user.ClientCertificate == "" && user.ClientKey == "" && user.TokenFile == ""
			if safe && tlsErr == nil {
				endpoint, host, endpointErr := address(u.Host, "443")
				if endpointErr == nil {
					item.Endpoint = endpoint
					tlsConfig.ServerName = host
					if cluster.TLSServerName != "" {
						tlsConfig.ServerName = cluster.TLSServerName
					}
					targets = append(targets, target{item, tlsConfig})
				} else {
					report.Certificates = append(report.Certificates, item)
				}
			} else {
				report.Certificates = append(report.Certificates, item)
			}
		}
	}
	timeout := s.Timeout
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 8 * time.Second
	}
	parallel := s.Parallelism
	if parallel <= 0 || parallel > 16 {
		parallel = 4
	}
	sem := make(chan struct{}, parallel)
	results := make([]Certificate, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t target) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = t.item
				results[i].Error = "Certificate check canceled"
				return
			}
			probe, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			results[i] = observe(probe, t, now)
		}(i, t)
	}
	wg.Wait()
	report.Certificates = append(report.Certificates, results...)
	sort.Slice(report.Certificates, func(i, j int) bool { return report.Certificates[i].ID < report.Certificates[j].ID })
	for _, item := range report.Certificates {
		switch item.Status {
		case "healthy":
			report.Summary.Healthy++
		case "warning":
			report.Summary.Warning++
		case "critical":
			report.Summary.Critical++
		default:
			report.Summary.Unknown++
		}
	}
	if report.Summary.Unknown > 0 {
		report.Status = "unknown"
	}
	if report.Summary.Warning > 0 {
		report.Status = "warning"
	}
	if report.Summary.Critical > 0 {
		report.Status = "critical"
	}
	return report
}
func observe(ctx context.Context, t target, now time.Time) Certificate {
	item := t.item
	cfg := t.config.Clone()
	cfg.Time = func() time.Time { return now }
	dialer := tls.Dialer{Config: cfg}
	conn, err := dialer.DialContext(ctx, "tcp", item.Endpoint)
	if err != nil {
		var verification *tls.CertificateVerificationError
		if errors.As(err, &verification) && len(verification.UnverifiedCertificates) > 0 {
			item = lifetime(item, verification.UnverifiedCertificates[0], now)
			item.Verification = "failed"
			item.Error = "Server certificate verification failed; dates are observed, not trusted"
			if item.Status == "healthy" {
				item.Status = "unknown"
				item.Reason = "verification-failed"
			}
		} else {
			item.Error = "TLS connection unavailable or timed out"
		}
		return item
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return item
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return item
	}
	item = lifetime(item, state.PeerCertificates[0], now)
	item.Verified = true
	item.Verification = "verified"
	item.Error = ""
	return item
}
