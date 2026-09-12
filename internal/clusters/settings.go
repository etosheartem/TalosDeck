package clusters

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

func recordAAD(scope, kind, key string) string {
	b, _ := json.Marshal([]string{scope, kind, key})
	return "private-record:" + string(b)
}
func validRecord(scope, kind, key string) error {
	if scope != "__fleet__" {
		if _, err := uuid.Parse(scope); err != nil {
			return errors.New("invalid record scope")
		}
	}
	for _, s := range []string{kind, key} {
		if s == "" || len(s) > 256 || strings.ContainsRune(s, 0) {
			return errors.New("invalid record key")
		}
	}
	return nil
}
func (s *Store) PutSecret(ctx context.Context, scope, kind, key string, data []byte) error {
	return s.putSecret(ctx, scope, kind, key, data, false)
}
func (s *Store) CreateSecret(ctx context.Context, scope, kind, key string, data []byte) error {
	return s.putSecret(ctx, scope, kind, key, data, true)
}
func (s *Store) putSecret(ctx context.Context, scope, kind, key string, data []byte, createOnly bool) error {
	if err := validRecord(scope, kind, key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	encrypted, err := s.encrypt(data, recordAAD(scope, kind, key))
	if err != nil {
		return err
	}
	q := `INSERT INTO private_records(scope,kind,name,payload) VALUES(?,?,?,?)`
	if !createOnly {
		q += ` ON CONFLICT(scope,kind,name) DO UPDATE SET payload=excluded.payload`
	}
	_, err = s.db.ExecContext(ctx, q, scope, kind, key, encrypted)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return ErrDuplicate
	}
	return err
}
func (s *Store) GetSecret(ctx context.Context, scope, kind, key string) ([]byte, error) {
	if err := validRecord(scope, kind, key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM private_records WHERE scope=? AND kind=? AND name=?`, scope, kind, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.decrypt(raw, recordAAD(scope, kind, key))
}
func (s *Store) DeleteSecret(ctx context.Context, scope, kind, key string) error {
	if err := validRecord(scope, kind, key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM private_records WHERE scope=? AND kind=? AND name=?`, scope, kind, key)
	return err
}
func (s *Store) ListSecretKeys(ctx context.Context, scope, kind string) ([]string, error) {
	if err := validRecord(scope, kind, "list"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM private_records WHERE scope=? AND kind=? ORDER BY name`, scope, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
func (s *Store) PutSetting(ctx context.Context, scope, kind, key string, data []byte) error {
	if err := validRecord(scope, kind, key); err != nil {
		return err
	}
	if !json.Valid(data) {
		return errors.New("setting must contain valid JSON")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(scope,kind,name,payload) VALUES(?,?,?,?) ON CONFLICT(scope,kind,name) DO UPDATE SET payload=excluded.payload`, scope, kind, key, data)
	return err
}
func (s *Store) GetSetting(ctx context.Context, scope, kind, key string) ([]byte, error) {
	if err := validRecord(scope, kind, key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM settings WHERE scope=? AND kind=? AND name=?`, scope, kind, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return data, err
}
func (s *Store) PutPrivateState(ctx context.Context, scope, key string, data []byte) error {
	return s.PutSecret(ctx, scope, "private-state", key, data)
}
func (s *Store) GetPrivateState(ctx context.Context, scope, key string) ([]byte, error) {
	return s.GetSecret(ctx, scope, "private-state", key)
}

func (s *Store) verifyPrivateRecords(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT scope,kind,name,payload FROM private_records`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var scope, kind, key string
		var raw []byte
		if err = rows.Scan(&scope, &kind, &key, &raw); err != nil {
			return err
		}
		if _, err = s.decrypt(raw, recordAAD(scope, kind, key)); err != nil {
			return errors.New("cannot decrypt private records with supplied key")
		}
	}
	return rows.Err()
}
func (s *Store) rotatePrivateRecords(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT scope,kind,name,payload FROM private_records`)
	if err != nil {
		return err
	}
	type record struct {
		scope, kind, key string
		data             []byte
	}
	var records []record
	for rows.Next() {
		var r record
		var raw []byte
		if err = rows.Scan(&r.scope, &r.kind, &r.key, &raw); err != nil {
			rows.Close()
			return err
		}
		plain, err := s.decrypt(raw, recordAAD(r.scope, r.kind, r.key))
		if err != nil {
			rows.Close()
			return err
		}
		r.data, err = s.encrypt(plain, recordAAD(r.scope, r.kind, r.key))
		if err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range records {
		if _, err = tx.ExecContext(ctx, `UPDATE private_records SET payload=? WHERE scope=? AND kind=? AND name=?`, r.data, r.scope, r.kind, r.key); err != nil {
			return err
		}
	}
	return nil
}
