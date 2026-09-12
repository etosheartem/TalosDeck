// Package clusters persists cluster metadata and encrypted infrastructure credentials.
package clusters

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("cluster or revision not found")
var ErrDuplicate = errors.New("cluster already exists")

type Cluster struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Endpoints         []string  `json:"endpoints"`
	Provider          string    `json:"provider"`
	TalosVersion      string    `json:"talosVersion"`
	KubernetesVersion string    `json:"kubernetesVersion"`
	Health            string    `json:"health"`
	CreatedAt         time.Time `json:"createdAt"`
	Identity          string    `json:"-"`
	Legacy            bool      `json:"-"`
}
type Credentials struct{ Talosconfig, Kubeconfig, ProviderSecrets []byte }
type Revision struct {
	ID        string    `json:"id"`
	ClusterID string    `json:"clusterId"`
	Node      string    `json:"node"`
	Author    string    `json:"author"`
	Mode      string    `json:"mode"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Config    []byte    `json:"-"`
}
type keyring struct {
	Active string            `json:"active"`
	Keys   map[string][]byte `json:"keys"`
}
type envelope struct {
	Key  string `json:"key"`
	Data string `json:"data"`
}
type Store struct {
	mu      sync.Mutex
	db      *sql.DB
	keys    keyring
	keyPath string
	dbPath  string
	lock    *os.File
}

func Open(dbPath, keyPath string) (*Store, error) {
	dbPath, _ = filepath.Abs(dbPath)
	keyPath, _ = filepath.Abs(keyPath)
	if dbPath == keyPath {
		return nil, errors.New("database and encryption key paths must differ")
	}
	_, statErr := os.Stat(dbPath)
	existing := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(dbPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("cluster database is already open by another process")
	}
	keepLock := false
	defer func() {
		if !keepLock {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			lock.Close()
		}
	}()
	var keys keyring
	data, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) && !existing {
		k := make([]byte, 32)
		if _, err = rand.Read(k); err != nil {
			return nil, err
		}
		id := uuid.NewString()
		keys = keyring{id, map[string][]byte{id: k}}
		data, _ = json.Marshal(keys)
		f, e := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return nil, e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ce != nil {
			return nil, ce
		}
	} else if err != nil {
		return nil, fmt.Errorf("encryption key unavailable: %w", err)
	} else if err = json.Unmarshal(data, &keys); err != nil {
		return nil, errors.New("invalid encryption key file")
	}
	if len(keys.Keys[keys.Active]) != 32 {
		return nil, errors.New("invalid encryption key")
	}
	for _, key := range keys.Keys {
		if len(key) != 32 {
			return nil, errors.New("invalid encryption key")
		}
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, err
	}
	// Read-only secret mounts cannot be chmod'ed; accept owner/group readable keys.
	if info.Mode().Perm()&0137 != 0 {
		if err = os.Chmod(keyPath, 0600); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err = os.Chmod(dbPath, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var schemaVersion int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		db.Close()
		return nil, err
	}
	if schemaVersion > 2 {
		db.Close()
		return nil, errors.New("cluster database schema is newer than this application")
	}
	s := &Store{db: db, keys: keys, keyPath: keyPath, dbPath: dbPath, lock: lock}
	_, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA journal_mode=DELETE;
 BEGIN IMMEDIATE;
 CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS clusters(id TEXT PRIMARY KEY,name TEXT NOT NULL,identity TEXT NOT NULL UNIQUE,metadata BLOB NOT NULL,credentials BLOB NOT NULL,legacy INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS revisions(id TEXT PRIMARY KEY,cluster_id TEXT NOT NULL REFERENCES clusters(id),node TEXT NOT NULL,metadata BLOB NOT NULL,config BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS revisions_node ON revisions(cluster_id,node);
 CREATE TABLE IF NOT EXISTS private_records(scope TEXT NOT NULL,kind TEXT NOT NULL,name TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(scope,kind,name));
 CREATE TABLE IF NOT EXISTS settings(scope TEXT NOT NULL,kind TEXT NOT NULL,name TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(scope,kind,name));
 INSERT OR IGNORE INTO schema_migrations VALUES(1);
 INSERT OR IGNORE INTO schema_migrations VALUES(2);
 PRAGMA user_version=2;
 COMMIT;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Fail closed immediately with a wrong key, rather than accepting unavailable clusters.
	rows, err := db.Query(`SELECT id,credentials FROM clusters`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for rows.Next() {
		var id string
		var b []byte
		if err = rows.Scan(&id, &b); err == nil {
			_, err = s.decrypt(b, "credentials:"+id)
		}
		if err != nil {
			rows.Close()
			db.Close()
			return nil, errors.New("cannot decrypt cluster credentials with supplied key")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = s.verifyPrivateRecords(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.interruptRevisions(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	keepLock = true
	return s, nil
}

func (s *Store) interruptRevisions(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,metadata FROM revisions`)
	if err != nil {
		return err
	}
	type update struct {
		id       string
		metadata []byte
	}
	var updates []update
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var r Revision
		if err = json.Unmarshal(raw, &r); err != nil {
			rows.Close()
			return err
		}
		if r.Status == "running" {
			r.Status = "interrupted"
			r.Error = "Application stopped before configuration result was confirmed; review node state before retrying"
			raw, _ = json.Marshal(r)
			updates = append(updates, update{id, raw})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		if _, err = tx.ExecContext(ctx, `UPDATE revisions SET metadata=? WHERE id=?`, u.metadata, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) encrypt(b []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(s.keys.Keys[s.keys.Active])
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	n := make([]byte, g.NonceSize())
	if _, err = rand.Read(n); err != nil {
		return nil, err
	}
	out := g.Seal(n, n, b, []byte(aad))
	return json.Marshal(envelope{s.keys.Active, base64.StdEncoding.EncodeToString(out)})
}
func (s *Store) decrypt(b []byte, aad string) ([]byte, error) {
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, errors.New("invalid encrypted record")
	}
	key := s.keys.Keys[e.Key]
	if len(key) != 32 {
		return nil, errors.New("encryption key unavailable")
	}
	block, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(block)
	raw, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil || len(raw) < g.NonceSize() {
		return nil, errors.New("invalid encrypted record")
	}
	p, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte(aad))
	if err != nil {
		return nil, errors.New("encrypted record authentication failed")
	}
	return p, nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.Close()
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
	return err
}
func (s *Store) List(ctx context.Context) ([]Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT metadata,legacy,identity FROM clusters ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Cluster{}
	for rows.Next() {
		var b []byte
		var c Cluster
		if err = rows.Scan(&b, &c.Legacy, &c.Identity); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) Get(ctx context.Context, id string) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	var c Cluster
	err := s.db.QueryRowContext(ctx, `SELECT metadata,identity,legacy FROM clusters WHERE id=?`, id).Scan(&b, &c.Identity, &c.Legacy)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	return c, err
}
func (s *Store) Create(ctx context.Context, c Cluster, credentials Credentials) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(credentials.Talosconfig) == 0 {
		return c, errors.New("cluster name and talosconfig are required")
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return c, errors.New("invalid cluster ID")
	}
	if c.Identity == "" {
		h := sha256.Sum256(credentials.Talosconfig)
		c.Identity = hex.EncodeToString(h[:])
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	b, _ := json.Marshal(c)
	plain, _ := json.Marshal(credentials)
	enc, err := s.encrypt(plain, "credentials:"+c.ID)
	if err != nil {
		return c, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO clusters(id,name,identity,metadata,credentials,legacy) VALUES(?,?,?,?,?,?)`, c.ID, c.Name, c.Identity, b, enc, c.Legacy)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return c, ErrDuplicate
	}
	return c, err
}
func (s *Store) Credentials(ctx context.Context, id string) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Credentials
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT credentials FROM clusters WHERE id=?`, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	p, err := s.decrypt(b, "credentials:"+id)
	if err == nil {
		err = json.Unmarshal(p, &c)
	}
	return c, err
}
func (s *Store) Update(ctx context.Context, c Cluster) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("cluster name required")
	}
	b, _ := json.Marshal(c)
	r, err := s.db.ExecContext(ctx, `UPDATE clusters SET name=?,metadata=?,legacy=? WHERE id=?`, c.Name, b, c.Legacy, c.ID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateCredentials(ctx context.Context, id string, credentials Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(credentials.Talosconfig) == 0 {
		return errors.New("talosconfig required")
	}
	plain, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	encrypted, err := s.encrypt(plain, "credentials:"+id)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE clusters SET credentials=? WHERE id=?`, encrypted, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) AddRevision(ctx context.Context, r Revision) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ClusterID == "" || r.Node == "" || len(r.Config) == 0 {
		return r, errors.New("cluster, node and config required")
	}
	r.ID = uuid.NewString()
	r.CreatedAt = time.Now().UTC()
	b, _ := json.Marshal(r)
	enc, err := s.encrypt(r.Config, "revision:"+r.ClusterID+":"+r.Node+":"+r.ID)
	if err != nil {
		return r, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO revisions VALUES(?,?,?,?,?)`, r.ID, r.ClusterID, r.Node, b, enc)
	return r, err
}
func (s *Store) ListRevisions(ctx context.Context, clusterID, node string) ([]Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT metadata FROM revisions WHERE cluster_id=? AND node=? ORDER BY rowid DESC`, clusterID, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var b []byte
		var r Revision
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) GetRevision(ctx context.Context, clusterID, node, id string) (Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Revision
	var b, enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT metadata,config FROM revisions WHERE cluster_id=? AND node=? AND id=?`, clusterID, node, id).Scan(&b, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	r.Config, err = s.decrypt(enc, "revision:"+clusterID+":"+node+":"+id)
	return r, err
}
func (s *Store) UpdateRevisionStatus(ctx context.Context, clusterID, node, id, status, errorText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT metadata FROM revisions WHERE cluster_id=? AND node=? AND id=?`, clusterID, node, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var r Revision
	if err = json.Unmarshal(b, &r); err != nil {
		return err
	}
	r.Status = status
	r.Error = errorText
	b, _ = json.Marshal(r)
	_, err = s.db.ExecContext(ctx, `UPDATE revisions SET metadata=? WHERE cluster_id=? AND node=? AND id=?`, b, clusterID, node, id)
	return err
}
func (s *Store) Backup(ctx context.Context, dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	f.Close()
	_, err = s.db.ExecContext(ctx, `VACUUM INTO ?`, dest)
	if err != nil {
		os.Remove(dest)
		return err
	}
	return os.Chmod(dest, 0600)
}
func writeKeys(path string, k keyring) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, _ := json.Marshal(k)
	f, err := os.CreateTemp(filepath.Dir(path), ".keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// RotateKey writes a recoverable keyring to both paths before committing re-encryption.
// Previous keys remain available for restoring older database backups.
func (s *Store) RotateKey(ctx context.Context, newKeyPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	newKeyPath, _ = filepath.Abs(newKeyPath)
	if newKeyPath == s.dbPath {
		return errors.New("key path cannot be database")
	}
	if newKeyPath != s.keyPath {
		if _, err := os.Lstat(newKeyPath); err == nil {
			return errors.New("new key path already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return err
	}
	next := keyring{uuid.NewString(), map[string][]byte{}}
	for id, key := range s.keys.Keys {
		next.Keys[id] = key
	}
	next.Keys[next.Active] = k
	if err := writeKeys(newKeyPath, next); err != nil {
		return err
	}
	if err := writeKeys(s.keyPath, next); err != nil {
		return err
	}
	s.keys = next
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"clusters", "revisions"} {
		query := `SELECT id,credentials,'' AS cluster_id,'' AS node FROM clusters`
		if table == "revisions" {
			query = `SELECT id,config,cluster_id,node FROM revisions`
		}
		rows, e := tx.QueryContext(ctx, query)
		if e != nil {
			return e
		}
		type record struct {
			id string
			b  []byte
		}
		var records []record
		for rows.Next() {
			var id, clusterID, node string
			var enc []byte
			if e = rows.Scan(&id, &enc, &clusterID, &node); e != nil {
				rows.Close()
				return e
			}
			aad := "credentials:" + id
			if table == "revisions" {
				aad = "revision:" + clusterID + ":" + node + ":" + id
			}
			plain, e := s.decrypt(enc, aad)
			if e != nil {
				rows.Close()
				return e
			}
			enc, e = s.encrypt(plain, aad)
			if e != nil {
				rows.Close()
				return e
			}
			records = append(records, record{id, enc})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, r := range records {
			q := `UPDATE clusters SET credentials=? WHERE id=?`
			if table == "revisions" {
				q = `UPDATE revisions SET config=? WHERE id=?`
			}
			if _, e = tx.ExecContext(ctx, q, r.b, r.id); e != nil {
				return e
			}
		}
	}
	if err = s.rotatePrivateRecords(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
