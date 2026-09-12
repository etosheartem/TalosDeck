package auth

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"talosdeck/internal/clusters"
)

type SecretStore interface {
	GetSecret(context.Context, string, string, string) ([]byte, error)
	PutSecret(context.Context, string, string, string, []byte) error
}
type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Provider  string    `json:"provider"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"createdAt"`
}
type userRecord struct {
	User
	PasswordHash string `json:"passwordHash,omitempty"`
	Version      uint64 `json:"version"`
	OIDCIdentity string `json:"oidcIdentity,omitempty"`
}
type persistentState struct {
	Users      map[string]*userRecord `json:"users"`
	SigningKey string                 `json:"signingKey"`
	Revoked    map[string]time.Time   `json:"revoked"`
}

var validUsername = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var ErrConflict = errors.New("user already exists or change would remove the last administrator")
var ErrUserNotFound = errors.New("user not found")

func ValidRole(role string) bool         { return role == "viewer" || role == "operator" || role == "admin" }
func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 72 }
func hashPassword(password string) (string, error) {
	if !validPassword(password) {
		return "", errors.New("password must contain 12–72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// NewPersistentAuthManager migrates the configured admin only on first startup.
// Later restarts never reset passwords or roles from environment variables.
func NewPersistentAuthManager(store SecretStore, adminPassword, jwtSecret string) (*AuthManager, error) {
	if store == nil {
		return nil, errors.New("encrypted authentication store required")
	}
	if jwtSecret != "" && len(jwtSecret) < 16 {
		return nil, errors.New("TALOSDECK_JWT_SECRET must contain at least 16 bytes")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := store.GetSecret(ctx, "__fleet__", "auth", "state")
	var state persistentState
	if errors.Is(err, clusters.ErrNotFound) {
		if adminPassword == "" {
			return nil, errors.New("TALOSDECK_ADMIN_PASSWORD is required to bootstrap the first administrator")
		}
		hash := adminPassword
		if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") && !strings.HasPrefix(hash, "$2y$") {
			hashBytes, e := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
			if e != nil {
				return nil, errors.New("cannot hash bootstrap password")
			}
			hash = string(hashBytes)
		} else if _, e := bcrypt.Cost([]byte(hash)); e != nil {
			return nil, errors.New("invalid bootstrap password hash")
		}
		if jwtSecret == "" {
			jwtSecret = RandomString(32)
		}
		user := &userRecord{User: User{ID: uuid.NewString(), Username: "admin", Role: "admin", Provider: "local", CreatedAt: time.Now().UTC()}, PasswordHash: hash, Version: 1}
		state = persistentState{Users: map[string]*userRecord{user.ID: user}, SigningKey: jwtSecret, Revoked: map[string]time.Time{}}
		raw, _ = json.Marshal(state)
		if err = store.PutSecret(ctx, "__fleet__", "auth", "state", raw); err != nil {
			return nil, errors.New("cannot persist initial administrator")
		}
	} else if err != nil {
		return nil, errors.New("cannot load encrypted authentication state")
	} else if err = json.Unmarshal(raw, &state); err != nil {
		return nil, errors.New("invalid authentication state")
	}
	if len(state.Users) == 0 || len(state.SigningKey) < 16 {
		return nil, errors.New("invalid persisted authentication state")
	}
	if state.Revoked == nil {
		state.Revoked = map[string]time.Time{}
	}
	// Explicit rotation of JWT signing key revokes all existing sessions.
	if jwtSecret != "" && jwtSecret != state.SigningKey {
		state.SigningKey = jwtSecret
		for _, u := range state.Users {
			u.Version++
		}
		raw, _ = json.Marshal(state)
		if err = store.PutSecret(ctx, "__fleet__", "auth", "state", raw); err != nil {
			return nil, errors.New("cannot rotate session signing key")
		}
	}
	a := NewAuthManager("persistent-managed", state.SigningKey)
	a.store = store
	a.state = &state
	return a, nil
}
func (a *AuthManager) Persistent() bool { return a != nil && a.store != nil }
func (a *AuthManager) findUsernameLocked(username string) *userRecord {
	if a.state == nil {
		return nil
	}
	for _, u := range a.state.Users {
		if u.Username == username {
			return u
		}
	}
	return nil
}
func (a *AuthManager) changeState(change func(*persistentState) error) error {
	a.usersMu.Lock()
	defer a.usersMu.Unlock()
	if a.state == nil {
		return errors.New("persistent users are not configured")
	}
	raw, _ := json.Marshal(a.state)
	var next persistentState
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	for id, expiry := range next.Revoked {
		if time.Now().After(expiry) {
			delete(next.Revoked, id)
		}
	}
	if err := change(&next); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = a.store.PutSecret(ctx, "__fleet__", "auth", "state", raw); err != nil {
		return errors.New("authentication change could not be persisted")
	}
	a.state = &next
	return nil
}
func (a *AuthManager) verifyLocal(username, password string) (User, error) {
	if username == "" {
		username = "admin"
	}
	if !a.Persistent() {
		if username == "admin" && a.VerifyPassword(password) {
			return User{Username: "admin", Role: "admin", Provider: "local"}, nil
		}
		return User{}, ErrInvalidCredentials
	}
	a.usersMu.RLock()
	u := a.findUsernameLocked(username)
	var user User
	hash := ""
	if u != nil && !u.Disabled && u.Provider == "local" {
		user = u.User
		hash = u.PasswordHash
	}
	a.usersMu.RUnlock()
	if hash == "" {
		hash = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5VnC2A6UYKUw7BlhlOnEL.VVaLVjlmG"
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || user.ID == "" {
		return User{}, ErrInvalidCredentials
	}
	return user, nil
}
func (a *AuthManager) Login(username, password string) (string, User, error) {
	u, err := a.verifyLocal(username, password)
	if err != nil {
		return "", u, err
	}
	token, err := a.GenerateToken(u.Username, u.Role)
	return token, u, err
}
func (a *AuthManager) ListUsers() []User {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	out := []User{}
	if a.state != nil {
		for _, u := range a.state.Users {
			out = append(out, u.User)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}
func (a *AuthManager) UserByClaims(c *Claims) User {
	a.usersMu.RLock()
	defer a.usersMu.RUnlock()
	if a.state != nil {
		if u := a.state.Users[c.UserID]; u != nil {
			return u.User
		}
	}
	return User{Username: c.Username, Role: c.Role, Provider: "local"}
}
func (a *AuthManager) CreateUser(username, password, role string) (User, error) {
	if !validUsername.MatchString(username) || !ValidRole(role) {
		return User{}, errors.New("invalid username or role")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := userRecord{User: User{ID: uuid.NewString(), Username: username, Role: role, Provider: "local", CreatedAt: time.Now().UTC()}, PasswordHash: hash, Version: 1}
	err = a.changeState(func(state *persistentState) error {
		for _, existing := range state.Users {
			if strings.EqualFold(existing.Username, username) {
				return ErrConflict
			}
		}
		state.Users[u.ID] = &u
		return nil
	})
	return u.User, err
}
func (a *AuthManager) UpdateUser(id string, role *string, disabled *bool) (User, error) {
	var result User
	err := a.changeState(func(state *persistentState) error {
		u := state.Users[id]
		if u == nil {
			return ErrUserNotFound
		}
		if role != nil {
			if !ValidRole(*role) {
				return errors.New("invalid role")
			}
			if u.Provider == "oidc" {
				return errors.New("OIDC roles are managed by identity provider group mappings")
			}
			u.Role = *role
		}
		if disabled != nil {
			u.Disabled = *disabled
		}
		admins := 0
		for _, user := range state.Users {
			if user.Role == "admin" && !user.Disabled && user.Provider == "local" {
				admins++
			}
		}
		if admins == 0 {
			return ErrConflict
		}
		u.Version++
		result = u.User
		return nil
	})
	return result, err
}
func (a *AuthManager) SetPassword(id, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return a.changeState(func(state *persistentState) error {
		u := state.Users[id]
		if u == nil {
			return ErrUserNotFound
		}
		if u.Provider != "local" {
			return errors.New("OIDC passwords are managed by the identity provider")
		}
		u.PasswordHash = hash
		u.Version++
		return nil
	})
}
func (a *AuthManager) RevokeUser(id string) error {
	return a.changeState(func(state *persistentState) error {
		u := state.Users[id]
		if u == nil {
			return ErrUserNotFound
		}
		u.Version++
		return nil
	})
}
func (a *AuthManager) ChangePassword(username, current, password string) error {
	u, err := a.verifyLocal(username, current)
	if err != nil {
		return err
	}
	return a.SetPassword(u.ID, password)
}
