package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"talosdeck/internal/executionauthority"
	"talosdeck/internal/reconcile"
)

// Opt-in acceptance against an independent SSH host. Only a fresh mktemp
// directory and its helper process are touched. The binary must implement
// `authority serve`; existing authority state must never be supplied here.
//
//	TD31_AUTHORITY_HOST=pve TD31_AUTHORITY_BINARY=/absolute/talosdeck go test \
//	  ./internal/jobs -run TestSSHAuthorityLostAfterPersistedIntent -v -count=1
func TestSSHAuthorityLostAfterPersistedIntent(t *testing.T) {
	host, binary := os.Getenv("TD31_AUTHORITY_HOST"), os.Getenv("TD31_AUTHORITY_BINARY")
	if host == "" || binary == "" {
		t.Skip("set TD31_AUTHORITY_HOST and TD31_AUTHORITY_BINARY for independent-host acceptance")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`).MatchString(host) || !regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`).MatchString(binary) {
		t.Fatal("unsafe SSH arguments")
	}
	ssh := func(command string, stdin string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ssh", "-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oConnectTimeout=5", host, command)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	remote, err := ssh("mktemp -d /tmp/td31-authority-acceptance.XXXXXXXX", "")
	if err != nil || !regexp.MustCompile(`^/tmp/td31-authority-acceptance\.[a-zA-Z0-9]+$`).MatchString(remote) {
		t.Fatalf("create isolated directory: %v", err)
	}
	defer func() {
		if _, err := ssh("rm -rf -- "+remote, ""); err != nil {
			t.Errorf("isolated fixture cleanup: %v", err)
		}
	}()
	script := "#!/bin/sh\necho $$ > " + remote + "/pid\nexec " + binary + " \"$@\"\n"
	if _, err = ssh("cat > "+remote+"/helper && chmod 700 "+remote+"/helper", script); err != nil {
		t.Fatal(err)
	}
	opts := executionauthority.SSHOptions{Host: host, RemoteBinary: remote + "/helper", StateDir: remote + "/state"}
	lease, err := executionauthority.OpenSSH(context.Background(), opts, "acceptance-old")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	dir := t.TempDir()
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer endpoint.Close()
	authority := &sshIntentFault{client: lease}
	authority.cut = func() error {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil || len(files) != 1 {
			return fmt.Errorf("expected one durable job: %v", err)
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			return err
		}
		var persisted Job
		if err = json.Unmarshal(raw, &persisted); err != nil {
			return err
		}
		if len(persisted.Intents) != 1 || persisted.Intents[0].Outcome != reconcile.Unknown {
			return fmt.Errorf("intent not persisted UNKNOWN before fault")
		}
		pid, err := ssh("cat "+remote+"/pid", "")
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(pid)
		if err != nil || n < 2 {
			return fmt.Errorf("invalid isolated helper PID")
		}
		_, err = ssh("kill -KILL "+pid, "")
		return err
	}
	runner := func(ctx context.Context, e *Execution, _ Request) error {
		if err := e.BeginIntent(ctx, "create-fixture", "create", reconcile.Identity{ProviderID: "fixture", ResourceID: "durable-resource", Generation: "1", OwnerID: "fixture-cluster"}); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	m, err := Open(dir, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.SetExecutionAuthority(authority, "acceptance-old", lease.Epoch); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	job, err := m.Submit(Request{Kind: "test-authority-fault"}, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("fault admission exceeded bounded deadline")
	}
	if authority.faultErr != nil {
		t.Fatal(authority.faultErr)
	}
	job, err = m.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !authority.cutDone || calls.Load() != 0 || job.Status != "interrupted" || len(job.Intents) != 1 || job.Intents[0].Outcome != reconcile.Unknown {
		t.Fatalf("unsafe result: cut=%v calls=%d status=%s intents=%+v", authority.cutDone, calls.Load(), job.Status, job.Intents)
	}
	if err = lease.Validate(context.Background(), "acceptance-old", lease.Epoch); err == nil {
		t.Fatal("dead handle regained admission")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	// A new SSH authority session and a fresh journal manager are not permission
	// to replay an ambiguous, previously admitted operation.
	opts.ExpectedEpoch = lease.Epoch
	fresh, err := executionauthority.OpenSSH(context.Background(), opts, "acceptance-new")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	var replay atomic.Int32
	reopened, err := Open(dir, func(context.Context, *Execution, Request) error { replay.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.SetExecutionAuthority(fresh, "acceptance-new", fresh.Epoch); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Get(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "interrupted" || recovered.Reviewed || len(recovered.Intents) != 1 || recovered.Intents[0].Outcome != reconcile.Unknown {
		t.Fatalf("restart lost uncertainty: %+v", recovered)
	}
	if _, err = reopened.Submit(Request{Kind: "test-authority-fault"}, "acceptance"); err == nil {
		t.Fatal("unreviewed interrupted job did not block submission")
	}
	if replay.Load() != 0 || calls.Load() != 0 {
		t.Fatal("restart replayed mutation")
	}
	t.Logf("PASS independent SSH helper killed after durable UNKNOWN intent; external HTTP calls=0; admission bounded=%s; epoch=%d->%d; reopened journal retains UNKNOWN/unreviewed; replay=0", time.Since(start).Round(time.Millisecond), lease.Epoch, fresh.Epoch)
}

type sshIntentFault struct {
	client      *executionauthority.Client
	validations int
	cut         func() error
	cutDone     bool
	faultErr    error
}

func (a *sshIntentFault) Validate(ctx context.Context, instance string, epoch uint64) error {
	a.validations++
	// Runner admission and pre-intent admission succeed; the post-fsync admission
	// executes only after the independently observed journal contains the intent.
	if a.validations == 3 {
		a.faultErr = a.cut()
		a.cutDone = a.faultErr == nil
		if a.faultErr != nil {
			return a.faultErr
		}
	}
	return a.client.Validate(ctx, instance, epoch)
}
