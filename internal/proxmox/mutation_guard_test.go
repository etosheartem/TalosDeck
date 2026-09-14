package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"talosdeck/internal/reconcile"
)

func TestMutationAdmissionAtHTTPDispatchPreservesReads(t *testing.T) {
	var writes atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		w.Write([]byte(`{"data":{}}`))
	}))
	defer ts.Close()
	c := &Client{cfg: Config{BaseURL: ts.URL, APIToken: "test"}, httpClient: ts.Client()}
	ctx := reconcile.WithMutationGuard(context.Background(), func(context.Context) error { return reconcile.ErrAuthority })
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if _, err := c.doRequest(ctx, method, "/nodes/pve/qemu/123", nil, ""); !errors.Is(err, reconcile.ErrAuthority) {
			t.Fatalf("%s: %v", method, err)
		}
	}
	if writes.Load() != 0 {
		t.Fatal("mutation reached provider")
	}
	resp, err := c.doRequest(ctx, http.MethodGet, "/nodes/pve/qemu/123", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
