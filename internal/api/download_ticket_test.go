package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"talosdeck/internal/auth"
	"talosdeck/internal/clusters"
)

func TestNativeDownloadTicketAuthenticatesBeforeFleetPathRewrite(t *testing.T) {
	f := newFleetFixture(t)
	id := uuid.NewString()
	child := fiber.New()
	child.Use(f.fleet.downloadTickets.Authenticate)
	child.Post("/api/backups/:id/download-ticket", auth.RequireAuth(f.auth), func(c *fiber.Ctx) error { return f.fleet.downloadTickets.Issue(c, id, c.Params("id")) })
	child.Get("/api/backups/:id/download", auth.RequireAuth(f.auth), func(c *fiber.Ctx) error { return c.SendString("scoped archive") })
	f.fleet.runtimes[id] = &clusterRuntime{handler: child.Handler()}
	path := "/api/clusters/" + id + "/backups/archive.tar.gz/download"
	req := httptest.NewRequest("POST", path+"-ticket", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	res, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || len(res.Cookies()) != 1 {
		t.Fatalf("scoped ticket status %d", res.StatusCode)
	}
	cookie := res.Cookies()[0]
	req = httptest.NewRequest("GET", path, nil)
	req.AddCookie(cookie)
	res, err = f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "scoped archive" {
		t.Fatalf("fleet rejected native capability before dispatch: %d %s", res.StatusCode, body)
	}
}

func TestBackupTransferTimeoutMatchesOnlyExactDownloadRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         time.Duration
	}{
		{"GET", "/api/backups/archive.tar.gz/download", 30 * time.Minute},
		{"GET", "/api/clusters/cluster-a/backups/archive.tar.gz/download", 30 * time.Minute},
		{"GET", "/api/clusters/cluster-a/backups/archive.tar.gz/download?ignored=value", 30 * time.Minute},
		{"POST", "/api/clusters/cluster-a/backups/archive.tar.gz/download", 0},
		{"GET", "/api/clusters/cluster-a/backups/archive.tar.gz/download-ticket", 0},
		{"GET", "/api/clusters/cluster-a/backups/archive.tar.gz/download/extra", 0},
		{"GET", "/api/clusters//backups/archive.tar.gz/download", 0},
		{"GET", "/api/backups//download", 0},
		{"GET", "/api/diagnostics/bundle", 0},
	} {
		var h fasthttp.RequestHeader
		h.SetMethod(tc.method)
		h.SetRequestURI(tc.path)
		if got := backupRequestConfig(&h).WriteTimeout; got != tc.want {
			t.Fatalf("%s %s timeout %s want %s", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestDownloadTicketsScopedSingleUseRevocableAndBounded(t *testing.T) {
	t.Setenv("TALOSDECK_TRUSTED_PROXIES", "")
	dir := t.TempDir()
	store, err := clusters.Open(filepath.Join(dir, "db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	am, err := auth.NewPersistentAuthManager(store, "fixture-admin-password", "")
	if err != nil {
		t.Fatal(err)
	}
	token, admin, err := am.Login("admin", "fixture-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	operator, err := am.CreateUser("operator", "fixture-operator-password", "operator")
	if err != nil {
		t.Fatal(err)
	}
	opToken, _, err := am.Login(operator.Username, "fixture-operator-password")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDownloadTickets(am)
	now := time.Now()
	d.now = func() time.Time { return now }
	app := fiber.New()
	app.Use(d.Authenticate)
	app.Post("/api/clusters/:cluster/backups/:id/download-ticket", auth.RequireAuth(am), func(c *fiber.Ctx) error { return d.Issue(c, c.Params("cluster"), c.Params("id")) })
	app.Get("/api/clusters/:cluster/backups/:id/download", auth.RequireAuth(am), func(c *fiber.Ctx) error { return c.SendStream(strings.NewReader("archive-stream")) })
	path := "/api/clusters/cluster-a/backups/backup.tar.gz/download"
	request := func(method, url, bearer string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req := httptest.NewRequest(method, url, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		req.Header.Set("X-Forwarded-Proto", "https") // Untrusted headers cannot set cookie transport policy.
		res, e := app.Test(req, -1)
		if e != nil {
			t.Fatal(e)
		}
		return res
	}
	issue := func(bearer string) *http.Cookie {
		t.Helper()
		res := request("POST", path+"-ticket", bearer, nil)
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("issue status %d", res.StatusCode)
		}
		body, _ := io.ReadAll(res.Body)
		if string(body) != "{\"ready\":true}" || strings.Contains(res.Header.Get("Set-Cookie"), bearer) {
			t.Fatal("session credential leaked")
		}
		cookies := res.Cookies()
		if len(cookies) != 1 {
			t.Fatal("missing capability cookie")
		}
		c := cookies[0]
		if c.Path != path || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 60 || c.Secure {
			t.Fatalf("unsafe capability cookie: %+v", c)
		}
		return c
	}
	res := request("POST", path+"-ticket", opToken, nil)
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("operator minted archive capability")
	}
	cookie := issue(token)
	res = request("GET", strings.Replace(path, "cluster-a", "cluster-b", 1), "", cookie)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("cross-cluster capability accepted")
	}
	res = request("GET", path, "", cookie)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "archive-stream" {
		t.Fatal("native authenticated download failed")
	}
	res = request("GET", path, "", cookie)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("capability replay accepted")
	}
	cookie = issue(token)
	now = now.Add(61 * time.Second)
	res = request("GET", path, "", cookie)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("expired capability accepted")
	}
	cookie = issue(token)
	if err = am.RevokeUser(admin.ID); err != nil {
		t.Fatal(err)
	}
	res = request("GET", path, "", cookie)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("revoked session capability accepted")
	}
	token, _, err = am.Login("admin", "fixture-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie = issue(token)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", path, nil)
			req.AddCookie(cookie)
			res, e := app.Test(req, -1)
			if e != nil {
				return
			}
			defer res.Body.Close()
			if res.StatusCode == 200 {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent replay: %d successful downloads", successes.Load())
	}
	for range downloadTicketLimit {
		issue(token)
	}
	res = request("POST", path+"-ticket", token, nil)
	res.Body.Close()
	if res.StatusCode != 429 {
		t.Fatal("ticket storage is unbounded")
	}
	now = now.Add(61 * time.Second)
	issue(token)
}
