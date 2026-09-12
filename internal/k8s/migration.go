package k8s

import (
	"errors"
	"os"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// PortableConfig embeds locally configured files during the one-time trusted
// installation migration. Uploaded configurations use NormalizeConfig instead.
func (m *K8sManager) PortableConfig() ([]byte, error) {
	c := m.credentialConfig
	if c == nil {
		return nil, errors.New("Kubernetes credentials are unavailable for migration")
	}
	if c.ExecProvider != nil || c.AuthProvider != nil {
		return nil, errors.New("migrating executable Kubernetes credentials is unsupported; supply a static kubeconfig")
	}
	read := func(data []byte, path string) ([]byte, error) {
		if len(data) > 0 || path == "" {
			return data, nil
		}
		return os.ReadFile(path)
	}
	ca, err := read(c.CAData, c.CAFile)
	if err != nil {
		return nil, errors.New("cannot read Kubernetes CA for migration")
	}
	crt, err := read(c.CertData, c.CertFile)
	if err != nil {
		return nil, errors.New("cannot read Kubernetes certificate for migration")
	}
	key, err := read(c.KeyData, c.KeyFile)
	if err != nil {
		return nil, errors.New("cannot read Kubernetes key for migration")
	}
	token := c.BearerToken
	if c.BearerTokenFile != "" {
		data, err := os.ReadFile(c.BearerTokenFile)
		if err != nil {
			return nil, errors.New("cannot read Kubernetes token for migration")
		}
		token = string(data)
	}
	config := clientcmdapi.Config{CurrentContext: "migrated", Clusters: map[string]*clientcmdapi.Cluster{"cluster": {Server: c.Host, CertificateAuthorityData: ca, TLSServerName: c.ServerName, InsecureSkipTLSVerify: c.Insecure}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"user": {Token: token, ClientCertificateData: crt, ClientKeyData: key}}, Contexts: map[string]*clientcmdapi.Context{"migrated": {Cluster: "cluster", AuthInfo: "user"}}}
	raw, err := clientcmd.Write(config)
	if err != nil {
		return nil, errors.New("cannot serialize Kubernetes credentials")
	}
	return NormalizeConfig(raw)
}
