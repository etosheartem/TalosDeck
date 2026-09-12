package k8s

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// NormalizeConfig accepts only self-contained credentials. Uploaded kubeconfigs
// must never execute programs or read arbitrary files on the management server.
func NormalizeConfig(data []byte) ([]byte, error) {
	return normalizeConfig(data, true)
}

func normalizeConfig(data []byte, requireCurrent bool) ([]byte, error) {
	cfg, err := clientcmd.Load(data)
	if err != nil || cfg.CurrentContext == "" {
		return nil, fmt.Errorf("kubeconfig: invalid YAML or missing current-context")
	}
	if err := clientcmdapi.MinifyConfig(cfg); err != nil {
		return nil, fmt.Errorf("kubeconfig: current context must reference an existing cluster and user")
	}
	if len(cfg.AuthInfos) != 1 || len(cfg.Clusters) != 1 {
		return nil, fmt.Errorf("kubeconfig: selected context must include a cluster and user")
	}
	for _, cluster := range cfg.Clusters {
		if cluster == nil {
			return nil, fmt.Errorf("kubeconfig: cluster configuration is missing")
		}
		u, err := url.Parse(cluster.Server)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("kubeconfig: server must be an HTTPS URL without credentials")
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("kubeconfig: invalid server port")
			}
		}
		if cluster.InsecureSkipTLSVerify || cluster.CertificateAuthority != "" || len(cluster.CertificateAuthorityData) == 0 || cluster.ProxyURL != "" {
			return nil, fmt.Errorf("kubeconfig: embed certificate-authority-data, enable TLS verification and remove proxy-url")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(cluster.CertificateAuthorityData) {
			return nil, fmt.Errorf("kubeconfig: invalid certificate-authority-data")
		}
	}
	for _, user := range cfg.AuthInfos {
		if user == nil {
			return nil, fmt.Errorf("kubeconfig: user credentials are missing")
		}
		if user.Exec != nil || user.AuthProvider != nil || user.TokenFile != "" || user.ClientCertificate != "" || user.ClientKey != "" || user.Impersonate != "" || user.ImpersonateUID != "" || len(user.ImpersonateGroups) > 0 || len(user.ImpersonateUserExtra) > 0 || user.Username != "" || user.Password != "" {
			return nil, fmt.Errorf("kubeconfig: exec, auth-provider, file references and impersonation are not supported; embed credentials")
		}
		if len(user.ClientCertificateData) > 0 || len(user.ClientKeyData) > 0 {
			pair, err := tls.X509KeyPair(user.ClientCertificateData, user.ClientKeyData)
			if err != nil {
				return nil, fmt.Errorf("kubeconfig: invalid embedded client certificate/key pair")
			}
			cert, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil || (requireCurrent && (time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter))) {
				return nil, fmt.Errorf("kubeconfig: client certificate expired or not yet valid")
			}
		}
		if user.Token == "" && (len(user.ClientCertificateData) == 0 || len(user.ClientKeyData) == 0) {
			return nil, fmt.Errorf("kubeconfig: embedded client certificate/key or token required")
		}
	}
	return clientcmd.Write(*cfg)
}

// NewK8sManagerFromBytes never falls back to environment or in-cluster auth.
func NewK8sManagerFromBytes(data []byte) (*K8sManager, error) {
	return newK8sManagerFromBytes(data, true)
}

// NewK8sManagerFromStoredBytes preserves access to monitoring when previously
// imported credentials expire. All structural restrictions and TLS verification
// remain enforced; remote APIs still reject expired client certificates.
func NewK8sManagerFromStoredBytes(data []byte) (*K8sManager, error) {
	return newK8sManagerFromBytes(data, false)
}

func newK8sManagerFromBytes(data []byte, requireCurrent bool) (*K8sManager, error) {
	normalized, err := normalizeConfig(data, requireCurrent)
	if err != nil {
		return nil, err
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(normalized)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: invalid embedded credentials")
	}
	cfg.Timeout = 8 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: unable to initialize TLS client")
	}
	return &K8sManager{clientset: cs, credentialConfig: rest.CopyConfig(cfg), cache: podCache{entries: make(map[string]podCacheEntry), ttl: 2 * time.Second}}, nil
}
