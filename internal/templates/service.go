package templates

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"regexp"
	"sort"
	"strings"
	"sync"
	"talosdeck/internal/clusters"
	"talosdeck/internal/operations"
	"talosdeck/internal/proxmox"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid template")
	ErrConflict = errors.New("template revision conflict")
	ErrNotFound = errors.New("template not found")
)

type Store interface {
	GetSecret(context.Context, string, string, string) ([]byte, error)
	PutSecret(context.Context, string, string, string, []byte) error
}
type MachineDefaults struct {
	Cores       int    `json:"cores"`
	MemoryMB    int    `json:"memoryMB"`
	DiskGB      int    `json:"diskGB"`
	Storage     string `json:"storage"`
	Bridge      string `json:"bridge"`
	VLAN        int    `json:"vlan"`
	NetworkMode string `json:"networkMode"`
}
type Spec struct {
	TalosVersion      string          `json:"talosVersion"`
	KubernetesVersion string          `json:"kubernetesVersion"`
	SchematicID       string          `json:"schematicId"`
	Architecture      string          `json:"architecture"`
	Platform          string          `json:"platform"`
	CNI               string          `json:"cni"`
	Storage           string          `json:"storage"`
	ControlPlanes     int             `json:"controlPlanes"`
	Workers           int             `json:"workers"`
	ControlPlane      MachineDefaults `json:"controlPlane"`
	Worker            MachineDefaults `json:"worker"`
}
type Template struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Archived       bool      `json:"archived"`
	LatestRevision int       `json:"latestRevision"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type Revision struct {
	TemplateID string    `json:"templateId"`
	Revision   int       `json:"revision"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	CreatedBy  string    `json:"createdBy"`
	SpecHash   string    `json:"specHash"`
	Spec       Spec      `json:"spec"`
}
type InstanceMachine struct {
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Address     string   `json:"address"`
	Gateway     string   `json:"gateway"`
	Nameservers []string `json:"nameservers"`
}
type Input struct {
	Name       string            `json:"name"`
	ProviderID string            `json:"providerId"`
	ISOStorage string            `json:"isoStorage"`
	Endpoint   string            `json:"endpoint"`
	Machines   []InstanceMachine `json:"machines"`
}
type state struct {
	Version   int                   `json:"version"`
	Templates map[string]Template   `json:"templates"`
	Revisions map[string][]Revision `json:"revisions"`
}
type Service struct {
	mu    sync.Mutex
	store Store
	state state
}

func Open(ctx context.Context, store Store) (*Service, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	s := &Service{store: store, state: state{Version: 1, Templates: map[string]Template{}, Revisions: map[string][]Revision{}}}
	b, e := store.GetSecret(ctx, "__fleet__", "templates", "state")
	if errors.Is(e, clusters.ErrNotFound) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	if len(b) > 8<<20 || json.Unmarshal(b, &s.state) != nil || s.state.Version != 1 || s.state.Templates == nil || s.state.Revisions == nil {
		return nil, ErrInvalid
	}
	for id, t := range s.state.Templates {
		rs := s.state.Revisions[id]
		if id != t.ID || len(rs) != t.LatestRevision {
			return nil, ErrInvalid
		}
		for i, r := range rs {
			if r.TemplateID != id || r.Revision != i+1 || r.SpecHash != hash(r.Spec) {
				return nil, ErrInvalid
			}
		}
	}
	return s, nil
}
func hash(spec Spec) string { b, _ := json.Marshal(spec); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func (s *Service) save(ctx context.Context, next state) error {
	b, e := json.Marshal(next)
	if e != nil || len(b) > 8<<20 {
		return ErrInvalid
	}
	if e = s.store.PutSecret(ctx, "__fleet__", "templates", "state", b); e != nil {
		return e
	}
	if json.Unmarshal(b, &next) != nil {
		return ErrInvalid
	}
	s.state = next
	return nil
}
func (s *Service) copy() state {
	b, _ := json.Marshal(s.state)
	var n state
	_ = json.Unmarshal(b, &n)
	return n
}
func (s *Service) List(archived bool) []Template {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Template{}
	for _, t := range s.state.Templates {
		if archived || !t.Archived {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out
}
func (s *Service) Get(id string) (Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.state.Templates[id]
	if !ok {
		return t, ErrNotFound
	}
	return t, nil
}
func (s *Service) GetRevision(id string, revision int) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.state.Revisions[id]
	if revision < 1 || revision > len(rs) {
		return Revision{}, ErrNotFound
	}
	return rs[revision-1], nil
}
func (s *Service) Revisions(id string) ([]Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.state.Revisions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]Revision{}, rs...), nil
}
func validName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 100 && !strings.ContainsAny(name, "\r\n\x00")
}
func (s *Service) Create(ctx context.Context, name string, spec Spec, user string) (Revision, error) {
	if !validName(name) || !validName(user) || Validate(spec) != nil {
		return Revision{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Templates) >= 100 {
		return Revision{}, ErrInvalid
	}
	now := time.Now().UTC()
	id := uuid.NewString()
	r := Revision{TemplateID: id, Revision: 1, Name: strings.Clone(name), CreatedAt: now, CreatedBy: strings.Clone(user), Spec: spec, SpecHash: hash(spec)}
	n := s.copy()
	n.Templates[id] = Template{ID: id, Name: r.Name, LatestRevision: 1, CreatedAt: now, UpdatedAt: now}
	n.Revisions[id] = []Revision{r}
	if e := s.save(ctx, n); e != nil {
		return Revision{}, e
	}
	return r, nil
}
func (s *Service) Revise(ctx context.Context, id string, expected int, name string, spec Spec, user string) (Revision, error) {
	if !validName(name) || !validName(user) || Validate(spec) != nil {
		return Revision{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.state.Templates[id]
	if !ok {
		return Revision{}, ErrNotFound
	}
	if t.LatestRevision != expected || t.Archived {
		return Revision{}, ErrConflict
	}
	if expected >= 100 {
		return Revision{}, ErrInvalid
	}
	r := Revision{TemplateID: id, Revision: expected + 1, Name: strings.Clone(name), CreatedAt: time.Now().UTC(), CreatedBy: strings.Clone(user), Spec: spec, SpecHash: hash(spec)}
	n := s.copy()
	t.Name = r.Name
	t.LatestRevision = r.Revision
	t.UpdatedAt = r.CreatedAt
	n.Templates[id] = t
	n.Revisions[id] = append(n.Revisions[id], r)
	if e := s.save(ctx, n); e != nil {
		return Revision{}, e
	}
	return r, nil
}
func (s *Service) Archive(ctx context.Context, id string, expected int, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.state.Templates[id]
	if !ok {
		return ErrNotFound
	}
	if t.LatestRevision != expected {
		return ErrConflict
	}
	n := s.copy()
	t.Archived = archived
	t.UpdatedAt = time.Now().UTC()
	n.Templates[id] = t
	return s.save(ctx, n)
}

var schematicID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Validate(s Spec) error {
	if (s.ControlPlanes != 1 && s.ControlPlanes != 3) || s.Workers < 0 || s.ControlPlanes+s.Workers > 100 || !schematicID.MatchString(s.SchematicID) || s.Architecture != "amd64" || s.Platform != "metal" {
		return ErrInvalid
	}
	in := Input{Name: "template-validation", ProviderID: uuid.NewString(), ISOStorage: "data"}
	for i := 0; i < s.ControlPlanes+s.Workers; i++ {
		role := "worker"
		d := s.Worker
		if i < s.ControlPlanes {
			role = "controlplane"
			d = s.ControlPlane
		}
		m := InstanceMachine{Name: fmt.Sprintf("node-%d", i), Role: role}
		if d.NetworkMode == "static" {
			m.Address = fmt.Sprintf("192.0.2.%d/24", i+1)
			m.Gateway = "192.0.2.254"
			m.Nameservers = []string{"192.0.2.254"}
		}
		in.Machines = append(in.Machines, m)
	}
	_, e := instantiate(s, in)
	return e
}
func InstantiateSpec(r Revision, in Input) (operations.ProvisionSpec, error) {
	if r.SpecHash != hash(r.Spec) || Validate(r.Spec) != nil {
		return operations.ProvisionSpec{}, ErrInvalid
	}
	return instantiate(r.Spec, in)
}
func instantiate(s Spec, in Input) (operations.ProvisionSpec, error) {
	p := operations.ProvisionSpec{Kind: "cluster-create", Name: in.Name, ProviderID: in.ProviderID, ISOStorage: in.ISOStorage, Endpoint: in.Endpoint, TalosVersion: s.TalosVersion, KubernetesVersion: s.KubernetesVersion, SchematicID: s.SchematicID, Architecture: s.Architecture, Platform: s.Platform, CNI: s.CNI, Storage: s.Storage, InstallerImage: "factory.talos.dev/metal-installer/" + s.SchematicID + ":v" + strings.TrimPrefix(s.TalosVersion, "v")}
	if len(in.Machines) != s.ControlPlanes+s.Workers {
		return p, ErrInvalid
	}
	cp, workers := 0, 0
	for _, m := range in.Machines {
		d := s.Worker
		switch m.Role {
		case "controlplane":
			cp++
			d = s.ControlPlane
		case "worker":
			workers++
		default:
			return p, ErrInvalid
		}
		p.Machines = append(p.Machines, proxmox.MachineSpec{Name: m.Name, Role: m.Role, Cores: d.Cores, MemoryMB: d.MemoryMB, DiskGB: d.DiskGB, Storage: d.Storage, Bridge: d.Bridge, VLAN: d.VLAN, NetworkMode: d.NetworkMode, Address: m.Address, Gateway: m.Gateway, Nameservers: append([]string{}, m.Nameservers...)})
	}
	if cp != s.ControlPlanes || workers != s.Workers {
		return p, ErrInvalid
	}
	if e := operations.ValidateProvision(p, operations.FleetScope); e != nil {
		return operations.ProvisionSpec{}, fmt.Errorf("%w: %s", ErrInvalid, e)
	}
	return p, nil
}
