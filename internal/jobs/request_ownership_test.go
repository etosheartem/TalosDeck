package jobs

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

// HTTP frameworks can lend handlers strings backed by their reusable buffers.
// Reuse the buffers only after the runner has started, while it is blocked, so
// the test checks ownership without introducing a concurrent data race.
func TestSubmitOwnsBorrowedRequestStrings(t *testing.T) {
	var request Request
	var buffers [][]byte
	value := reflect.ValueOf(&request).Elem()
	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).Kind() != reflect.String {
			continue
		}
		buf := []byte("original-" + value.Type().Field(i).Name)
		buffers = append(buffers, buf)
		value.Field(i).SetString(unsafe.String(unsafe.SliceData(buf), len(buf)))
	}
	userBytes := []byte("original-operator")
	user := unsafe.String(unsafe.SliceData(userBytes), len(userBytes))
	expected := request
	expectedValue := reflect.ValueOf(&expected).Elem()
	for i := 0; i < expectedValue.NumField(); i++ {
		if expectedValue.Field(i).Kind() == reflect.String {
			expectedValue.Field(i).SetString(strings.Clone(expectedValue.Field(i).String()))
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	received := make(chan Request, 1)
	m, err := Open(t.TempDir(), func(_ context.Context, _ *Execution, r Request) error {
		close(started)
		<-release
		received <- r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	j, err := m.Submit(request, user)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	<-started
	for _, buf := range append(buffers, userBytes) {
		for i := range buf {
			buf[i] = 'x'
		}
	}
	close(release)
	if got := <-received; !reflect.DeepEqual(got, expected) {
		t.Fatalf("runner retained borrowed request: got %+v, want %+v", got, expected)
	}
	done := waitJob(t, m, j.ID, "succeeded")
	if !reflect.DeepEqual(done.Request, expected) || done.User != "original-operator" {
		t.Fatalf("journal retained borrowed strings: %+v", done)
	}
}
