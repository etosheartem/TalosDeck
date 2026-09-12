package k8s

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
)

// Manifests are pinned at build time. Provisioning never fetches executable
// YAML or chart templates supplied by a request.
//
//go:embed addons/*
var addonFiles embed.FS

func addonManifest(kind, endpoint string) ([]byte, error) {
	if kind != "cilium" && kind != "local-path" {
		return nil, errors.New("unsupported cluster addon")
	}
	data, err := addonFiles.ReadFile("addons/" + kind + ".yaml")
	if err != nil {
		return nil, err
	}
	if kind == "cilium" {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return nil, errors.New("invalid Kubernetes endpoint")
		}
		data = bytes.ReplaceAll(data, []byte("TALOSDECK_API_HOST"), []byte(u.Hostname()))
		port := u.Port()
		if port == "" {
			port = "443"
		}
		data = bytes.ReplaceAll(data, []byte(`value: "6443"`), []byte(`value: "`+port+`"`))
	}
	return data, nil
}
func (m *K8sManager) InstallAddon(ctx context.Context, kind string) error {
	data, err := addonManifest(kind, m.credentialConfig.Host)
	if err != nil {
		return err
	}
	client, err := dynamic.NewForConfig(m.credentialConfig)
	if err != nil {
		return err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(m.credentialConfig)
	if err != nil {
		return err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var object unstructured.Unstructured
		err = decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid bundled addon manifest")
		}
		if object.GetKind() == "" {
			continue
		}
		if object.GetKind() == "Secret" {
			return errors.New("bundled addon must not contain shared credentials")
		}
		mapping, err := mapper.RESTMapping(object.GroupVersionKind().GroupKind(), object.GroupVersionKind().Version)
		if err != nil {
			return fmt.Errorf("addon resource API unavailable: %s", object.GetKind())
		}
		var resource dynamic.ResourceInterface = client.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			resource = client.Resource(mapping.Resource).Namespace(object.GetNamespace())
		}
		encoded, err := json.Marshal(object.Object)
		if err != nil {
			return err
		}
		if _, err = resource.Patch(ctx, object.GetName(), types.ApplyPatchType, encoded, metav1.PatchOptions{FieldManager: "talosdeck-provisioning"}); err != nil {
			return fmt.Errorf("cannot apply addon resource %s/%s", object.GetKind(), object.GetName())
		}
	}
	return nil
}
func (m *K8sManager) AddonReady(ctx context.Context, kind string) bool {
	if kind == "local-path" {
		d, err := m.clientset.AppsV1().Deployments("local-path-storage").Get(ctx, "local-path-provisioner", metav1.GetOptions{})
		return err == nil && d.Status.ObservedGeneration >= d.Generation && d.Status.AvailableReplicas >= 1
	}
	if kind == "cilium" {
		d, err := m.clientset.AppsV1().DaemonSets("kube-system").Get(ctx, "cilium", metav1.GetOptions{})
		if err != nil || d.Status.ObservedGeneration < d.Generation || d.Status.DesiredNumberScheduled == 0 || d.Status.NumberReady != d.Status.DesiredNumberScheduled {
			return false
		}
		op, err := m.clientset.AppsV1().Deployments("kube-system").Get(ctx, "cilium-operator", metav1.GetOptions{})
		return err == nil && op.Status.AvailableReplicas >= 1
	}
	return strings.TrimSpace(kind) == "" || kind == "none" || kind == "flannel"
}
