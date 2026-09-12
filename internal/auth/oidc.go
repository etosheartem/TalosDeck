package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	Issuer, ClientID, ClientSecret, RedirectURL, Name string
	GroupsClaim                                       string
	GroupRoles                                        map[string]string
	AllowHTTP                                         bool
}
type oidcFlow struct {
	Nonce, Verifier string
	Expires         time.Time
}
type oidcExchange struct {
	Binding, Token string
	User           User
	Expires        time.Time
}
type OIDC struct {
	mu        sync.Mutex
	config    OIDCConfig
	oauth     oauth2.Config
	verifier  *coreoidc.IDTokenVerifier
	client    *http.Client
	flows     map[string]oidcFlow
	exchanges map[string]oidcExchange
}

func oidcURL(value string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid OIDC URL")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if !allowHTTP || u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("OIDC URLs must use HTTPS; HTTP is allowed only for explicit loopback development")
		}
	}
	return u, nil
}
func (a *AuthManager) ConfigureOIDC(ctx context.Context, cfg OIDCConfig) error {
	if !a.Persistent() {
		return errors.New("OIDC requires persistent user storage")
	}
	if cfg.Issuer == "" {
		a.oidc = nil
		return nil
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return errors.New("OIDC client ID and client secret are required")
	}
	if _, err := oidcURL(cfg.Issuer, cfg.AllowHTTP); err != nil {
		return err
	}
	redirect, err := oidcURL(cfg.RedirectURL, cfg.AllowHTTP)
	if err != nil || redirect.Path != "/api/auth/oidc/callback" {
		return errors.New("OIDC redirect URL must be the public /api/auth/oidc/callback endpoint")
	}
	for _, role := range cfg.GroupRoles {
		if !ValidRole(role) {
			return errors.New("invalid OIDC group role mapping")
		}
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if cfg.Name == "" {
		cfg.Name = "Single sign-on"
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many OIDC redirects")
		}
		_, err := oidcURL(req.URL.String(), cfg.AllowHTTP)
		return err
	}}
	ctx = coreoidc.ClientContext(ctx, client)
	provider, err := coreoidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return errors.New("OIDC discovery failed; check issuer URL, TLS and connectivity")
	}
	var discovery struct {
		JWKS string `json:"jwks_uri"`
	}
	if err = provider.Claims(&discovery); err != nil {
		return errors.New("invalid OIDC discovery document")
	}
	endpoint := provider.Endpoint()
	for _, value := range []string{endpoint.AuthURL, endpoint.TokenURL, discovery.JWKS} {
		if _, err = oidcURL(value, cfg.AllowHTTP); err != nil {
			return errors.New("OIDC discovery endpoints must use trusted HTTPS URLs")
		}
	}
	a.oidc = &OIDC{config: cfg, oauth: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: endpoint, RedirectURL: cfg.RedirectURL, Scopes: []string{coreoidc.ScopeOpenID, "profile", "email"}}, verifier: provider.Verifier(&coreoidc.Config{ClientID: cfg.ClientID}), client: client, flows: map[string]oidcFlow{}, exchanges: map[string]oidcExchange{}}
	return nil
}
func (a *AuthManager) ConfigureOIDCFromEnv(ctx context.Context) error {
	cfg := OIDCConfig{Issuer: os.Getenv("TALOSDECK_OIDC_ISSUER"), ClientID: os.Getenv("TALOSDECK_OIDC_CLIENT_ID"), ClientSecret: os.Getenv("TALOSDECK_OIDC_CLIENT_SECRET"), RedirectURL: os.Getenv("TALOSDECK_OIDC_REDIRECT_URL"), Name: os.Getenv("TALOSDECK_OIDC_NAME"), GroupsClaim: os.Getenv("TALOSDECK_OIDC_GROUPS_CLAIM"), AllowHTTP: os.Getenv("TALOSDECK_OIDC_ALLOW_HTTP") == "true"}
	if mapping := os.Getenv("TALOSDECK_OIDC_GROUP_ROLES"); mapping != "" {
		if err := json.Unmarshal([]byte(mapping), &cfg.GroupRoles); err != nil {
			return errors.New("invalid TALOSDECK_OIDC_GROUP_ROLES JSON")
		}
	}
	return a.ConfigureOIDC(ctx, cfg)
}
func (a *AuthManager) OIDCEnabled() bool { return a != nil && a.oidc != nil }
func (a *AuthManager) OIDCName() string {
	if !a.OIDCEnabled() {
		return "Single sign-on"
	}
	return a.oidc.config.Name
}
func (a *AuthManager) OIDCSecureCookie() bool {
	return a.OIDCEnabled() && strings.HasPrefix(a.oidc.config.RedirectURL, "https://")
}
func (a *AuthManager) OIDCOrigin() string {
	if !a.OIDCEnabled() {
		return ""
	}
	u, _ := url.Parse(a.oidc.config.RedirectURL)
	return u.Scheme + "://" + u.Host
}
func (o *OIDC) cleanup() {
	now := time.Now()
	for state, flow := range o.flows {
		if now.After(flow.Expires) {
			delete(o.flows, state)
		}
	}
	for code, exchange := range o.exchanges {
		if now.After(exchange.Expires) {
			delete(o.exchanges, code)
		}
	}
}
func (a *AuthManager) BeginOIDC() (location, state string, err error) {
	if !a.OIDCEnabled() {
		return "", "", errors.New("OIDC is not configured")
	}
	o := a.oidc
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanup()
	if len(o.flows) >= 1000 {
		return "", "", errors.New("too many pending OIDC logins")
	}
	state = RandomString(32)
	flow := oidcFlow{Nonce: RandomString(32), Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(5 * time.Minute)}
	o.flows[state] = flow
	location = o.oauth.AuthCodeURL(state, coreoidc.Nonce(flow.Nonce), oauth2.S256ChallengeOption(flow.Verifier))
	return location, state, nil
}
func (a *AuthManager) FinishOIDC(ctx context.Context, state, cookie, code string) (exchangeCode, binding string, err error) {
	if !a.OIDCEnabled() || state == "" || code == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookie)) != 1 {
		return "", "", ErrInvalidCredentials
	}
	o := a.oidc
	o.mu.Lock()
	flow, ok := o.flows[state]
	delete(o.flows, state)
	o.mu.Unlock()
	if !ok || time.Now().After(flow.Expires) {
		return "", "", ErrInvalidCredentials
	}
	ctx = coreoidc.ClientContext(ctx, o.client)
	token, err := o.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return "", "", errors.New("OIDC authorization code exchange failed")
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		return "", "", errors.New("OIDC provider did not return an ID token")
	}
	verified, err := o.verifier.Verify(ctx, rawID)
	if err != nil || verified.Subject == "" || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(flow.Nonce)) != 1 {
		return "", "", errors.New("OIDC identity or nonce validation failed")
	}
	var claims map[string]any
	if err = verified.Claims(&claims); err != nil {
		return "", "", ErrInvalidCredentials
	}
	role := "viewer"
	if groups, ok := claims[o.config.GroupsClaim].([]any); ok {
		for _, g := range groups {
			group, ok := g.(string)
			if !ok {
				continue
			}
			mapped := o.config.GroupRoles[group]
			if mapped == "admin" || (mapped == "operator" && role != "admin") {
				role = mapped
			}
		}
	}
	username, _ := claims["preferred_username"].(string)
	if username == "" {
		username, _ = claims["email"].(string)
	}
	user, err := a.oidcUser(o.config.Issuer, verified.Subject, username, role)
	if err != nil {
		return "", "", err
	}
	signed, err := a.GenerateToken(user.Username, user.Role)
	if err != nil {
		return "", "", err
	}
	exchangeCode, binding = RandomString(32), RandomString(32)
	o.mu.Lock()
	o.cleanup()
	o.exchanges[exchangeCode] = oidcExchange{Binding: binding, Token: signed, User: user, Expires: time.Now().Add(time.Minute)}
	o.mu.Unlock()
	return exchangeCode, binding, nil
}
func (a *AuthManager) ExchangeOIDC(code, binding string) (string, User, error) {
	if !a.OIDCEnabled() {
		return "", User{}, ErrInvalidCredentials
	}
	o := a.oidc
	o.mu.Lock()
	defer o.mu.Unlock()
	value, ok := o.exchanges[code]
	if !ok || time.Now().After(value.Expires) || subtle.ConstantTimeCompare([]byte(value.Binding), []byte(binding)) != 1 {
		return "", User{}, ErrInvalidCredentials
	}
	delete(o.exchanges, code)
	if _, err := a.ValidateToken(value.Token); err != nil {
		return "", User{}, err
	}
	return value.Token, value.User, nil
}
func (a *AuthManager) oidcUser(issuer, subject, name, role string) (User, error) {
	hash := sha256.Sum256([]byte(issuer + "\x00" + subject))
	identity := hex.EncodeToString(hash[:])
	var result User
	err := a.changeState(func(state *persistentState) error {
		for _, u := range state.Users {
			if u.OIDCIdentity == identity {
				if u.Disabled {
					return ErrInvalidCredentials
				}
				if u.Role != role {
					u.Role = role
					u.Version++
				}
				result = u.User
				return nil
			}
		}
		clean := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '_' {
				return r
			}
			return '_'
		}, name)
		if len(clean) > 40 {
			clean = clean[:40]
		}
		if clean == "" {
			clean = "user"
		}
		username := "oidc-" + clean + "-" + identity[:8]
		u := &userRecord{User: User{ID: uuid.NewString(), Username: username, Role: role, Provider: "oidc", CreatedAt: time.Now().UTC()}, Version: 1, OIDCIdentity: identity}
		state.Users[u.ID] = u
		result = u.User
		return nil
	})
	return result, err
}
