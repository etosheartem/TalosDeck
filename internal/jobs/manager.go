// Package jobs runs one durable cluster operation at a time. Checkpoints are
// committed before side effects. A process restart never replays a command.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"talosdeck/internal/reconcile"
)

var ErrBusy = errors.New("another operation is running or requires review")
var ErrStopped = errors.New("stop requested; no further steps will be started")
var ErrNotFound = errors.New("job not found")
var ErrUncertain = errors.New("operation outcome is uncertain; inspect cluster state")

const MaxEvents = 2000
const MaxJobs = 100

type Request struct {
	BackupID         string `json:"backupId,omitempty"`
	BackupType       string `json:"backupType,omitempty"`
	TargetID         string `json:"targetId,omitempty"`
	RestorePlanID    string `json:"restorePlanId,omitempty"`
	DedupeKey        string `json:"dedupeKey,omitempty"`
	Kind             string `json:"kind"`
	Version          string `json:"version,omitempty"`
	AllowDowntime    bool   `json:"allowDowntime,omitempty"`
	ConfigRevisionID string `json:"configRevisionId,omitempty"`
	Node             string `json:"node,omitempty"`
	ProvisionID      string `json:"provisionId,omitempty"`
}
type Event struct {
	Time    time.Time `json:"time"`
	Step    string    `json:"step"`
	Message string    `json:"message"`
}
type Job struct {
	WorkflowVersion       int                `json:"workflowVersion,omitempty"`
	PlanVersion           int                `json:"planVersion,omitempty"`
	StepSchemaVersion     int                `json:"stepSchemaVersion,omitempty"`
	ReconciliationOutcome string             `json:"reconciliationOutcome,omitempty"`
	Intents               []reconcile.Intent `json:"intents,omitempty"`
	ID                    string             `json:"id"`
	ClusterID             string             `json:"clusterId,omitempty"`
	Request               Request            `json:"request"`
	User                  string             `json:"user"`
	Status                string             `json:"status"`
	CreatedAt             time.Time          `json:"createdAt"`
	UpdatedAt             time.Time          `json:"updatedAt"`
	Step                  string             `json:"step"`
	Error                 string             `json:"error,omitempty"`
	StopRequested         bool               `json:"stopRequested"`
	Reviewed              bool               `json:"reviewed"`
	Events                []Event            `json:"events,omitempty"`
}
type Runner func(context.Context, *Execution, Request) error

type Manager struct {
	authorityMu   sync.RWMutex
	authority     reconcile.Authority
	instanceID    string
	executorEpoch uint64
	mu            sync.Mutex
	dir           string
	lock          *os.File
	jobs          map[string]*Job
	active        string
	manual        bool
	closed        bool
	storageErr    error
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	runner        Runner
	onComplete    func(Job)
	clusterID     string
	closeOnce     sync.Once
	closeErr      error
}

// SetCompletionHandler installs a bounded observer for audit/notifications.
// It runs outside the manager lock and cannot change the persisted job result.
func (m *Manager) SetCompletionHandler(handler func(Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onComplete = handler
}

func Open(dir string, runner Runner) (*Manager, error) {
	return OpenCluster(dir, "", runner)
}

// OpenCluster binds the durable journal to one cluster. Legacy records are
// migrated in place; a journal already belonging to another cluster is rejected.
func OpenCluster(dir, clusterID string, runner Runner) (*Manager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("job store already in use: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{dir: dir, clusterID: clusterID, lock: lock, jobs: map[string]*Job{}, ctx: ctx, cancel: cancel, runner: runner}
	fail := func(err error) (*Manager, error) { cancel(); lock.Close(); return nil, err }
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return fail(err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return fail(err)
		}
		var j Job
		if err = json.Unmarshal(data, &j); err != nil {
			return fail(fmt.Errorf("corrupt job store %s: %w", filepath.Base(path), err))
		}
		if _, err = uuid.Parse(j.ID); err != nil || filepath.Base(path) != j.ID+".json" {
			return fail(errors.New("invalid job ID in store"))
		}
		if j.ClusterID != "" && j.ClusterID != clusterID {
			return fail(errors.New("job journal belongs to a different cluster"))
		}
		j.ClusterID = clusterID
		switch j.Status {
		case "queued", "running":
			j.Status = "interrupted"
			j.ReconciliationOutcome = reconcile.Unknown
			j.Error = "Server stopped before completion was verified. Inspect the cluster before acknowledging this job."
			j.UpdatedAt = time.Now().UTC()
		case "succeeded", "failed", "stopped", "interrupted":
		default:
			return fail(errors.New("invalid persisted job status"))
		}
		if j.WorkflowVersion != reconcile.Version || j.PlanVersion != reconcile.Version || j.StepSchemaVersion != reconcile.Version {
			j.ReconciliationOutcome = reconcile.RequiresReview
		}
		m.jobs[j.ID] = &j
		if err = m.save(&j); err != nil {
			return fail(err)
		}
	}
	return m, nil
}
func (m *Manager) save(j *Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.dir, ".job-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(m.dir, j.ID+".json")); err != nil {
		return err
	}
	d, err := os.Open(m.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func clone(j *Job) Job {
	c := *j
	c.Events = append([]Event(nil), j.Events...)
	c.Intents = append([]reconcile.Intent(nil), j.Intents...)
	for n := range c.Intents {
		if c.Intents[n].Evidence != nil {
			v := *c.Intents[n].Evidence
			c.Intents[n].Evidence = &v
		}
	}
	return c
}
func (m *Manager) busy() bool {
	if m.active != "" || m.manual {
		return true
	}
	for _, j := range m.jobs {
		if j.Status == "interrupted" && !j.Reviewed {
			return true
		}
	}
	return false
}
func (m *Manager) available() error {
	if m.closed {
		return errors.New("job manager is shutting down")
	}
	if m.storageErr != nil {
		return fmt.Errorf("job journal unavailable: %w", m.storageErr)
	}
	if m.busy() {
		return ErrBusy
	}
	return nil
}

// ReserveManual closes the race between legacy synchronous mutations and job submission.
func (m *Manager) ReserveManual() (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.available(); err != nil {
		return nil, err
	}
	if err := m.validateAuthority(m.ctx); err != nil {
		return nil, err
	}
	m.manual = true
	return func() { m.mu.Lock(); m.manual = false; m.mu.Unlock() }, nil
}
func (m *Manager) Submit(r Request, user string) (Job, error) {
	// Fiber parameters may alias reusable request buffers. The journal and
	// asynchronous runner must own their strings after the handler returns.
	encoded, err := json.Marshal(r)
	if err != nil {
		return Job{}, err
	}
	var owned Request
	if err = json.Unmarshal(encoded, &owned); err != nil {
		return Job{}, err
	}
	r = owned
	user = strings.Clone(user)
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.DedupeKey != "" {
		for _, j := range m.jobs {
			if j.Request.DedupeKey == r.DedupeKey && j.Request.Kind == r.Kind {
				return clone(j), nil
			}
		}
	}
	if err := m.available(); err != nil {
		return Job{}, err
	}
	if m.runner == nil {
		return Job{}, errors.New("job runner unavailable")
	}
	if len(m.jobs) >= MaxJobs {
		var oldest *Job
		for _, j := range m.jobs {
			if oldest == nil || j.CreatedAt.Before(oldest.CreatedAt) {
				oldest = j
			}
		}
		if err := os.Remove(filepath.Join(m.dir, oldest.ID+".json")); err != nil {
			return Job{}, err
		}
		delete(m.jobs, oldest.ID)
	}
	now := time.Now().UTC()
	j := &Job{WorkflowVersion: 1, PlanVersion: 1, StepSchemaVersion: 1, ID: uuid.NewString(), ClusterID: m.clusterID, Request: r, User: user, Status: "queued", CreatedAt: now, UpdatedAt: now, Events: []Event{{Time: now, Step: "queued", Message: "Job accepted"}}}
	if err := m.save(j); err != nil {
		return Job{}, err
	}
	m.jobs[j.ID] = j
	m.active = j.ID
	m.wg.Add(1)
	go m.run(j.ID)
	return clone(j), nil
}
func (m *Manager) run(id string) {
	defer m.wg.Done()
	e := &Execution{manager: m, id: id}
	var runErr error
	panicked := false
	defer func() {
		if p := recover(); p != nil {
			panicked = true
			runErr = errors.New("operation interrupted unexpectedly; inspect cluster state")
		}
		m.mu.Lock()
		j := m.jobs[id]
		for _, intent := range j.Intents {
			if intent.Outcome != "succeeded" {
				runErr = ErrUncertain
				break
			}
		}
		switch {
		case m.ctx.Err() != nil || m.storageErr != nil || panicked || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) || errors.Is(runErr, ErrUncertain):
			j.Status = "interrupted"
			j.ReconciliationOutcome = reconcile.Unknown
		case errors.Is(runErr, ErrStopped):
			j.Status = "stopped"
		case runErr != nil:
			j.Status = "failed"
		default:
			j.Status = "succeeded"
		}
		if runErr != nil {
			j.Error = bounded(runErr.Error())
		}
		j.UpdatedAt = time.Now().UTC()
		j.Events = appendEvent(j.Events, Event{Time: j.UpdatedAt, Step: j.Status, Message: j.Error})
		if err := m.save(j); err != nil {
			m.storageErr = err
			j.Status = "interrupted"
			j.Error = "Cannot persist completion: " + err.Error()
		}
		m.active = ""
		completed, handler := clone(j), m.onComplete
		m.mu.Unlock()
		if handler != nil {
			func() { defer func() { _ = recover() }(); handler(completed) }()
		}
	}()
	m.mu.Lock()
	j := m.jobs[id]
	j.Status = "running"
	j.UpdatedAt = time.Now().UTC()
	r := j.Request
	runErr = m.save(j)
	if runErr != nil {
		m.storageErr = runErr
	}
	m.mu.Unlock()
	if runErr != nil {
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 4*time.Hour)
	defer cancel()
	if runErr = m.validateAuthority(ctx); runErr != nil {
		runErr = fmt.Errorf("%w: %v", ErrUncertain, runErr)
		return
	}
	runErr = m.runner(ctx, e, r)
}
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		c := clone(j)
		c.Events = nil
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
func (m *Manager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return clone(j), nil
}
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.Status != "running" && j.Status != "queued" {
		return errors.New("job is not running")
	}
	j.StopRequested = true
	j.UpdatedAt = time.Now().UTC()
	j.Events = appendEvent(j.Events, Event{Time: j.UpdatedAt, Step: j.Step, Message: "Stop requested; the current step will finish first"})
	if err := m.save(j); err != nil {
		m.storageErr = err
		return err
	}
	return nil
}
func (m *Manager) Acknowledge(id, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.Status != "interrupted" {
		return errors.New("only interrupted jobs require acknowledgement")
	}
	j.Reviewed = true
	j.UpdatedAt = time.Now().UTC()
	j.Events = appendEvent(j.Events, Event{Time: j.UpdatedAt, Step: "reviewed", Message: "Cluster state reviewed by " + user})
	if err := m.save(j); err != nil {
		m.storageErr = err
		return err
	}
	return nil
}
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		m.mu.Unlock()
		m.wg.Wait()
		m.closeErr = m.lock.Close()
	})
	return m.closeErr
}

type Execution struct {
	manager *Manager
	id      string
}

var sensitiveOutput = regexp.MustCompile(`(?i)(authorization|bearer\s|password|private[ _-]?key|client[ _-]?key|client-certificate-data|\btoken\b|\bsecret\b)`)

func bounded(s string) string {
	if sensitiveOutput.MatchString(s) {
		return "[redacted sensitive operation output]"
	}
	s = strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' {
			return ' '
		}
		return r
	}, s)
	if len(s) > 4096 {
		s = s[:4096] + "…"
	}
	return s
}
func appendEvent(events []Event, e Event) []Event {
	events = append(events, e)
	if len(events) > MaxEvents {
		events = events[len(events)-MaxEvents:]
	}
	return events
}
func (e *Execution) Log(step, message string) error {
	m := e.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storageErr != nil {
		return m.storageErr
	}
	j := m.jobs[e.id]
	j.Step = step
	j.UpdatedAt = time.Now().UTC()
	j.Events = appendEvent(j.Events, Event{Time: j.UpdatedAt, Step: step, Message: bounded(message)})
	if err := m.save(j); err != nil {
		m.storageErr = err
		return err
	}
	return nil
}
func (e *Execution) Checkpoint(ctx context.Context, step, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := e.manager
	m.mu.Lock()
	stop := m.jobs[e.id].StopRequested
	m.mu.Unlock()
	if stop {
		return ErrStopped
	}
	if err := m.validateAuthority(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	if err := e.Log(step, message); err != nil {
		return err
	}
	// Persistence can outlive a lease; admission is checked again after fsync.
	if err := m.validateAuthority(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	return nil
}
