package k8s

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ConfigIdentity compares the Kubernetes trust roots rather than addresses:
// different private networks often reuse exactly the same IPs and node names.
func ConfigIdentity(raw []byte) (string, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil || clientcmdapi.MinifyConfig(cfg) != nil {
		return "", errors.New("cannot identify kubeconfig CA")
	}
	var roots []string
	for _, cluster := range cfg.Clusters {
		data := cluster.CertificateAuthorityData
		for len(data) > 0 {
			block, rest := pem.Decode(data)
			if block == nil {
				break
			}
			data = rest
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil || !cert.IsCA {
				return "", errors.New("invalid Kubernetes CA certificate")
			}
			sum := sha256.Sum256(cert.Raw)
			roots = append(roots, hex.EncodeToString(sum[:]))
		}
	}
	if len(roots) == 0 {
		return "", errors.New("kubeconfig must embed its Kubernetes CA")
	}
	sort.Strings(roots)
	return strings.Join(roots, ":"), nil
}
