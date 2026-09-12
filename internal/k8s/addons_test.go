package k8s

import (
	"bytes"
	"io"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"strings"
	"testing"
)

func TestPinnedAddonManifestsHaveNoSharedCredentials(t *testing.T) {
	for _, kind := range []string{"cilium", "local-path"} {
		t.Run(kind, func(t *testing.T) {
			data, err := addonManifest(kind, "https://api.example.test:7443")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("TALOSDECK_API_HOST")) {
				t.Fatal("endpoint not substituted")
			}
			decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
			count := 0
			for {
				var object unstructured.Unstructured
				err := decoder.Decode(&object)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if object.GetKind() == "Secret" {
					t.Fatal("shared private material in manifest")
				}
				if object.GetKind() != "" {
					count++
				}
			}
			if count < 5 {
				t.Fatal("incomplete addon bundle")
			}
			if kind == "cilium" && (!strings.Contains(string(data), `value: "7443"`) || strings.Contains(string(data), `value: "6443"`)) {
				t.Fatal("wrong API port")
			}
		})
	}
}
