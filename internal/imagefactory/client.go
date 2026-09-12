// Package imagefactory supports the non-secret, official-extension-only schematic subset.
package imagefactory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go.yaml.in/yaml/v4"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Extension struct {
	Name   string `json:"name"`
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}
type Versions struct {
	Versions  []string  `json:"versions"`
	CheckedAt time.Time `json:"checkedAt"`
	Stale     bool      `json:"stale"`
}
type Catalog struct {
	Version    string      `json:"version"`
	Extensions []Extension `json:"extensions"`
	CheckedAt  time.Time   `json:"checkedAt"`
	Stale      bool        `json:"stale"`
}
type Selection struct {
	Version      string   `json:"version"`
	Architecture string   `json:"architecture"`
	Platform     string   `json:"platform"`
	Extensions   []string `json:"extensions"`
}
type Profile struct {
	ID             string      `json:"id"`
	Version        string      `json:"version"`
	Architecture   string      `json:"architecture"`
	Platform       string      `json:"platform"`
	Extensions     []Extension `json:"extensions"`
	InstallerImage string      `json:"installerImage"`
	ISOURL         string      `json:"isoURL"`
	CheckedAt      time.Time   `json:"checkedAt"`
}
type cacheEntry struct {
	body []byte
	at   time.Time
}
type Client struct {
	base  string
	http  *http.Client
	mu    sync.Mutex
	cache map[string]cacheEntry
	slots chan struct{}
	ttl   time.Duration
}

var ErrInvalid = errors.New("invalid image selection")

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

var stable = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var identifier = regexp.MustCompile(`^[a-f0-9]{64}$`)
var extensionName = regexp.MustCompile(`^siderolabs/[a-z0-9][a-z0-9._-]{0,127}$`)

func NewClient() *Client { return newClient("https://factory.talos.dev", &http.Client{}) }
func newClient(base string, h *http.Client) *Client {
	return &Client{base: base, http: h, cache: map[string]cacheEntry{}, slots: make(chan struct{}, 4), ttl: 5 * time.Minute}
}
func (c *Client) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, errors.New("image factory request timed out")
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if e != nil {
		return nil, errors.New("invalid factory request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/yaml")
	}
	h := *c.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, e := h.Do(req)
	if e != nil {
		return nil, errors.New("image factory unavailable")
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return nil, errors.New("image factory rejected request")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 2<<20+1))
	if e != nil || len(b) > 2<<20 {
		return nil, errors.New("invalid factory response size")
	}
	return b, nil
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if d.Decode(v) != nil {
		return errors.New("invalid factory response")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("invalid factory response")
	}
	return nil
}
func (c *Client) fetch(ctx context.Context, path string, fresh bool) (cacheEntry, bool, error) {
	c.mu.Lock()
	entry, ok := c.cache[path]
	c.mu.Unlock()
	if !fresh && ok && time.Since(entry.at) < c.ttl {
		return entry, false, nil
	}
	b, e := c.request(ctx, "GET", path, nil)
	if e != nil {
		if !fresh && ok {
			return entry, true, nil
		}
		return cacheEntry{}, false, e
	}
	return cacheEntry{body: b, at: time.Now().UTC()}, false, nil
}
func (c *Client) remember(path string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= 32 {
		var oldest string
		var at time.Time
		for k, v := range c.cache {
			if oldest == "" || v.at.Before(at) {
				oldest = k
				at = v.at
			}
		}
		delete(c.cache, oldest)
	}
	e.body = bytes.Clone(e.body)
	c.cache[path] = e
}
func (c *Client) versions(ctx context.Context, fresh bool) (Versions, error) {
	e, stale, err := c.fetch(ctx, "/versions", fresh)
	if err != nil {
		return Versions{}, err
	}
	v := Versions{CheckedAt: e.at, Stale: stale}
	var raw []string
	if decode(e.body, &raw) != nil || len(raw) > 2000 {
		return v, errors.New("invalid versions catalog")
	}
	for _, s := range raw {
		if stable.MatchString(s) {
			v.Versions = append(v.Versions, s)
		}
	}
	if len(v.Versions) == 0 {
		return v, errors.New("empty stable versions catalog")
	}
	if !stale {
		c.remember("/versions", e)
	}
	return v, nil
}
func (c *Client) Versions(ctx context.Context) (Versions, error) { return c.versions(ctx, false) }
func (c *Client) catalog(ctx context.Context, version string, fresh bool) (Catalog, error) {
	if !stable.MatchString(version) {
		return Catalog{}, invalid("explicit stable Talos version required")
	}
	path := "/version/" + version + "/extensions/official"
	e, stale, err := c.fetch(ctx, path, fresh)
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{Version: version, CheckedAt: e.at, Stale: stale}
	if decode(e.body, &out.Extensions) != nil || out.Extensions == nil || len(out.Extensions) > 1000 {
		return out, errors.New("invalid extension catalog")
	}
	seen := map[string]bool{}
	for _, x := range out.Extensions {
		if !extensionName.MatchString(x.Name) || seen[x.Name] || !strings.HasPrefix(x.Digest, "sha256:") || !identifier.MatchString(strings.TrimPrefix(x.Digest, "sha256:")) || len(x.Ref) > 512 || !strings.HasPrefix(x.Ref, "ghcr.io/siderolabs/") {
			return Catalog{}, errors.New("invalid extension metadata")
		}
		seen[x.Name] = true
	}
	if !stale {
		c.remember(path, e)
	}
	return out, nil
}
func (c *Client) Extensions(ctx context.Context, version string) (Catalog, error) {
	return c.catalog(ctx, version, false)
}

type schematic struct {
	Customization struct {
		SystemExtensions struct {
			OfficialExtensions []string `yaml:"officialExtensions,omitempty"`
		} `yaml:"systemExtensions,omitempty"`
	} `yaml:"customization"`
}

func schematicID(s schematic) (string, []byte, error) {
	b, e := yaml.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, e
}
func parseSchematic(b []byte) (schematic, error) {
	var s schematic
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if d.Decode(&s) != nil {
		return s, invalid("unsupported schematic customization")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return s, invalid("invalid schematic document")
	}
	return s, nil
}
func (c *Client) profile(ctx context.Context, id, version string, s schematic) (Profile, error) {
	v, e := c.versions(ctx, true)
	if e != nil {
		return Profile{}, e
	}
	found := false
	for _, x := range v.Versions {
		found = found || x == version
	}
	if !found {
		return Profile{}, invalid("Talos version unavailable")
	}
	catalog, e := c.catalog(ctx, version, true)
	if e != nil {
		return Profile{}, e
	}
	p := Profile{ID: id, Version: version, Architecture: "amd64", Platform: "metal", Extensions: []Extension{}, CheckedAt: time.Now().UTC(), InstallerImage: "factory.talos.dev/metal-installer/" + id + ":" + version, ISOURL: c.base + "/image/" + id + "/" + version + "/metal-amd64.iso"}
	names := s.Customization.SystemExtensions.OfficialExtensions
	if len(names) > 64 {
		return Profile{}, invalid("too many extensions")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return Profile{}, invalid("duplicate schematic extension")
		}
		seen[name] = true
		ok := false
		for _, x := range catalog.Extensions {
			if x.Name == name {
				p.Extensions = append(p.Extensions, x)
				ok = true
				break
			}
		}
		if !ok {
			return Profile{}, invalid("extension unavailable for selected version")
		}
	}
	return p, nil
}
func (c *Client) Resolve(ctx context.Context, id, version string) (Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if !identifier.MatchString(id) || !stable.MatchString(version) {
		return Profile{}, invalid("invalid schematic ID or version")
	}
	b, e := c.request(ctx, "GET", "/schematics/"+id, nil)
	if e != nil {
		return Profile{}, e
	}
	s, e := parseSchematic(b)
	if e != nil {
		return Profile{}, e
	}
	actual, _, e := schematicID(s)
	if e != nil || actual != id {
		return Profile{}, invalid("schematic identity mismatch")
	}
	return c.profile(ctx, id, version, s)
}
func (c *Client) Create(ctx context.Context, selection Selection) (Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if selection.Architecture != "amd64" || selection.Platform != "metal" || !stable.MatchString(selection.Version) {
		return Profile{}, invalid("supported image is metal amd64 with stable version")
	}
	var s schematic
	seen := map[string]bool{}
	for _, name := range selection.Extensions {
		if !extensionName.MatchString(name) {
			return Profile{}, invalid("invalid extension name")
		}
		if !seen[name] {
			s.Customization.SystemExtensions.OfficialExtensions = append(s.Customization.SystemExtensions.OfficialExtensions, name)
			seen[name] = true
		}
	}
	sort.Strings(s.Customization.SystemExtensions.OfficialExtensions)
	id, b, e := schematicID(s)
	if e != nil {
		return Profile{}, e
	}
	if _, e = c.profile(ctx, id, selection.Version, s); e != nil {
		return Profile{}, e
	}
	r, e := c.request(ctx, "POST", "/schematics", b)
	if e != nil {
		return Profile{}, e
	}
	var response struct {
		ID string `json:"id"`
	}
	if decode(r, &response) != nil || response.ID != id {
		return Profile{}, invalid("created schematic identity mismatch")
	}
	return c.Resolve(ctx, id, selection.Version)
}

// Checksum streams the canonical asset. It never accepts a caller-supplied download URL.
func (c *Client) Checksum(ctx context.Context, p Profile) (string, error) {
	if !identifier.MatchString(p.ID) || !stable.MatchString(p.Version) || p.Platform != "metal" || p.Architecture != "amd64" {
		return "", invalid("invalid image profile")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return "", errors.New("image download canceled")
	}
	target := c.base + "/image/" + p.ID + "/" + p.Version + "/metal-amd64.iso"
	req, _ := http.NewRequestWithContext(ctx, "GET", target, nil)
	h := *c.http
	h.Timeout = 0
	h.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		base, _ := url.Parse(c.base)
		allowed := req.URL.Scheme == base.Scheme && req.URL.Host == base.Host
		if base.Host == "factory.talos.dev" && req.URL.Scheme == "https" && req.URL.Hostname() == "assets.factory.talos.dev" && (req.URL.Port() == "" || req.URL.Port() == "443") {
			allowed = true
		}
		if len(via) > 3 || !allowed || req.URL.User != nil {
			return errors.New("untrusted image redirect")
		}
		return nil
	}
	r, e := h.Do(req)
	if e != nil {
		return "", errors.New("image download failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return "", errors.New("image download rejected")
	}
	hash := sha256.New()
	n, e := io.Copy(hash, io.LimitReader(r.Body, (2<<30)+1))
	if e != nil || n == 0 || n > 2<<30 {
		return "", errors.New("image download incomplete or exceeds 2 GiB")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
