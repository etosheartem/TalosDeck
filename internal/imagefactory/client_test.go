package imagefactory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCanonicalVanillaID(t *testing.T) {
	id, _, e := schematicID(schematic{})
	if e != nil || id != "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba" {
		t.Fatal(id, e)
	}
}
func TestCreateResolveChecksum(t *testing.T) {
	var sc schematic
	sc.Customization.SystemExtensions.OfficialExtensions = []string{"siderolabs/qemu-guest-agent"}
	id, body, _ := schematicID(sc)
	var posted int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/versions":
			fmt.Fprint(w, `["v1.14.0","v1.15.0-alpha.1"]`)
		case strings.Contains(r.URL.Path, "/extensions/"):
			fmt.Fprintf(w, `[{"name":"siderolabs/qemu-guest-agent","ref":"ghcr.io/siderolabs/qemu-guest-agent:1","digest":"sha256:%s"}]`, strings.Repeat("a", 64))
		case r.URL.Path == "/schematics" && r.Method == "POST":
			posted++
			fmt.Fprintf(w, `{"id":%q}`, id)
		case r.URL.Path == "/schematics/"+id:
			w.Write(body)
		case strings.HasSuffix(r.URL.Path, ".iso"):
			fmt.Fprint(w, "fixture ISO")
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := newClient(s.URL, s.Client())
	p, e := c.Create(context.Background(), Selection{Version: "v1.14.0", Platform: "metal", Architecture: "amd64", Extensions: []string{"siderolabs/qemu-guest-agent", "siderolabs/qemu-guest-agent"}})
	if e != nil || p.ID != id || len(p.Extensions) != 1 || posted != 1 {
		t.Fatal(p, e)
	}
	p.ISOURL = "http://attacker.invalid/secret"
	sum, e := c.Checksum(context.Background(), p)
	if e != nil || sum != fmt.Sprintf("%x", sha256.Sum256([]byte("fixture ISO"))) {
		t.Fatal(sum, e)
	}
}
func TestReadCacheStaleButResolveFailsClosed(t *testing.T) {
	fail := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `["v1.14.0"]`)
	}))
	defer s.Close()
	c := newClient(s.URL, s.Client())
	c.ttl = time.Nanosecond
	if _, e := c.Versions(context.Background()); e != nil {
		t.Fatal(e)
	}
	fail = true
	v, e := c.Versions(context.Background())
	if e != nil || !v.Stale {
		t.Fatal("stale cache absent")
	}
	if _, e = c.versions(context.Background(), true); e == nil {
		t.Fatal("mutation used stale catalog")
	}
}
func TestSchematicRejectsSecretsAndMultipleDocuments(t *testing.T) {
	for _, b := range []string{"customization:\n  embeddedMachineConfiguration: secret", "customization: {}\n---\ncustomization: {}", "owner: secret\ncustomization: {}", "customization:\n  extraKernelArgs: [secret]"} {
		if _, e := parseSchematic([]byte(b)); e == nil {
			t.Fatal("unsupported schematic accepted")
		}
	}
}
func TestFactoryRejectsIdentityMismatchAndInvalidVersion(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "customization: {}\n") }))
	defer s.Close()
	c := newClient(s.URL, s.Client())
	if _, e := c.Resolve(context.Background(), strings.Repeat("a", 64), "v1.14.0"); e == nil {
		t.Fatal("identity mismatch accepted")
	}
	for _, v := range []string{"latest", "v1.14.0/../../x", "v1.14.0-rc.1"} {
		if _, e := c.Extensions(context.Background(), v); e == nil {
			t.Fatal("invalid version accepted")
		}
	}
}
func TestFactoryNoRedirectAndBoundedResponse(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://example.com")
		w.WriteHeader(302)
	}))
	defer s.Close()
	c := newClient(s.URL, s.Client())
	if _, e := c.Versions(context.Background()); e == nil {
		t.Fatal("redirect accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.Versions(ctx); e == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestInvalidAndOversizedCatalogNeverCached(t *testing.T) {
	for _, body := range []string{`{"versions":[]}`, `["v1.14.0"] []`, strings.Repeat("x", (2<<20)+1)} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c := newClient(s.URL, s.Client())
		if _, e := c.Versions(context.Background()); e == nil {
			t.Fatal("invalid response accepted")
		}
		if len(c.cache) != 0 {
			t.Fatal("invalid response cached")
		}
		s.Close()
	}
}
func TestUnknownExtensionRejectedBeforeSchematicPOST(t *testing.T) {
	posts := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		if r.URL.Path == "/versions" {
			fmt.Fprint(w, `["v1.14.0"]`)
		} else {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer s.Close()
	c := newClient(s.URL, s.Client())
	_, e := c.Create(context.Background(), Selection{Version: "v1.14.0", Architecture: "amd64", Platform: "metal", Extensions: []string{"siderolabs/not-present"}})
	if e == nil || posts != 0 {
		t.Fatal("unsupported extension sent to factory")
	}
}

func TestLiveResolveSchematic(t *testing.T) {
	id := os.Getenv("TALOSDECK_TEST_FACTORY_SCHEMATIC")
	if id == "" {
		t.Skip("opt-in read-only Factory resolve")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p, e := NewClient().Resolve(ctx, id, "v1.14.0")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("verified official canonical schematic=%s version=%s extensions=%d", p.ID, p.Version, len(p.Extensions))
}

type imageRoundTrip func(*http.Request) (*http.Response, error)

func (f imageRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestChecksumRedirectAllowlist(t *testing.T) {
	for _, host := range []string{"assets.factory.talos.dev", "assets.factory.talos.dev.evil.invalid", "evil.invalid", "assets.factory.talos.dev:444"} {
		t.Run(host, func(t *testing.T) {
			h := &http.Client{Transport: imageRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "factory.talos.dev" {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://" + host + "/fixture"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ISO")), Request: r}, nil
			})}
			c := newClient("https://factory.talos.dev", h)
			_, e := c.Checksum(context.Background(), Profile{ID: strings.Repeat("a", 64), Version: "v1.14.0", Architecture: "amd64", Platform: "metal"})
			if (e == nil) != (host == "assets.factory.talos.dev") {
				t.Fatal("redirect trust mismatch", e)
			}
		})
	}
}
