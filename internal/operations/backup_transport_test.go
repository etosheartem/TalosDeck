package operations

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"talosdeck/internal/reconcile"
	"testing"
)

type backupTestTransport func(*http.Request) (*http.Response, error)

func (f backupTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBackupTransportAuthorityBeforeEachPart(t *testing.T) {
	calls := 0
	transport := backupMutationTransport{backupTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})}
	admitted := 0
	ctx := reconcile.WithMutationRecorder(context.Background(), func(ctx context.Context, action, target string, call func() error) error {
		admitted++
		if !strings.HasPrefix(action, "backup.s3-") {
			t.Fatal(action)
		}
		if admitted > 1 {
			return reconcile.ErrAuthority
		}
		return call()
	})
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		req, _ := http.NewRequestWithContext(ctx, method, "https://example.invalid/bucket/object?uploadId=opaque", nil)
		_, err := transport.RoundTrip(req)
		if admitted > 1 && !errors.Is(err, reconcile.ErrAuthority) {
			t.Fatal("part executed without authority", err)
		}
	}
	if calls != 1 {
		t.Fatalf("external calls %d", calls)
	}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", "https://example.invalid/bucket/object", nil)
	if _, err := transport.RoundTrip(req); err != nil || calls != 2 || admitted != 3 {
		t.Fatal("observation unexpectedly mutates", err)
	}
}
