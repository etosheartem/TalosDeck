package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type fakeOIDC struct {
	mu                     sync.Mutex
	server                 *httptest.Server
	key                    *rsa.PrivateKey
	nonce, challenge, mode string
	pkceChecked            bool
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOIDC{key: key}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": fake.server.URL, "authorization_endpoint": fake.server.URL + "/authorize", "token_endpoint": fake.server.URL + "/token", "jwks_uri": fake.server.URL + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		case "/token":
			r.ParseForm()
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			fake.pkceChecked = base64.RawURLEncoding.EncodeToString(challenge[:]) == fake.challenge
			id, secret, ok := r.BasicAuth()
			if !ok || id != "talosdeck" || secret != "oidc-client-secret" || !fake.pkceChecked {
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
				return
			}
			claims := jwt.MapClaims{"iss": fake.server.URL, "sub": "immutable-subject", "aud": "talosdeck", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "nonce": fake.nonce, "preferred_username": "artem", "groups": []string{"operators"}}
			switch fake.mode {
			case "nonce":
				claims["nonce"] = "wrong"
			case "audience":
				claims["aud"] = "other-client"
			case "issuer":
				claims["iss"] = "https://other-issuer.example"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "unmapped":
				claims["groups"] = []string{"not-configured"}
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "test"
			signed, err := token.SignedString(key)
			if err != nil {
				t.Error(err)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "provider-access-token-must-not-escape", "token_type": "Bearer", "id_token": signed, "expires_in": 60})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}
func (f *fakeOIDC) begin(t *testing.T, a *AuthManager, mode string) string {
	t.Helper()
	location, state, err := a.BeginOIDC()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE S256 missing")
	}
	f.mu.Lock()
	f.nonce = u.Query().Get("nonce")
	f.challenge = u.Query().Get("code_challenge")
	f.mode = mode
	f.mu.Unlock()
	return state
}

func TestOIDCVerifiedPKCECookieBoundExchangeAndReplay(t *testing.T) {
	manager, _, _, _ := persistentFixture(t)
	provider := newFakeOIDC(t)
	err := manager.ConfigureOIDC(context.Background(), OIDCConfig{Issuer: provider.server.URL, ClientID: "talosdeck", ClientSecret: "oidc-client-secret", RedirectURL: provider.server.URL + "/api/auth/oidc/callback", AllowHTTP: true, GroupRoles: map[string]string{"operators": "operator"}})
	if err != nil {
		t.Fatal(err)
	}
	state := provider.begin(t, manager, "")
	if _, _, err = manager.FinishOIDC(context.Background(), state, "wrong-cookie", "code"); err == nil {
		t.Fatal("state cookie bypass")
	}
	code, binding, err := manager.FinishOIDC(context.Background(), state, state, "code")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = manager.FinishOIDC(context.Background(), state, state, "code"); err == nil {
		t.Fatal("authorization flow replay")
	}
	if _, _, err = manager.ExchangeOIDC(code, "wrong-cookie"); err == nil {
		t.Fatal("sign-in code usable without browser binding")
	}
	token, user, err := manager.ExchangeOIDC(code, binding)
	if err != nil {
		t.Fatal(err)
	}
	if user.Provider != "oidc" || user.Role != "operator" || user.Username == "admin" {
		t.Fatalf("OIDC user mapping: %+v", user)
	}
	if _, err = manager.ValidateToken(token); err != nil {
		t.Fatal(err)
	}
	if _, _, err = manager.ExchangeOIDC(code, binding); err == nil {
		t.Fatal("exchange code replay")
	}
	if _, _, err = manager.Login(user.Username, "oidc-client-secret"); err == nil {
		t.Fatal("OIDC user has local password")
	}
	if err = manager.RevokeUser(user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.ValidateToken(token); err == nil {
		t.Fatal("OIDC application session survived revocation")
	}
	for _, mode := range []string{"nonce", "audience", "issuer", "expired"} {
		state = provider.begin(t, manager, mode)
		if _, _, err = manager.FinishOIDC(context.Background(), state, state, "code"); err == nil {
			t.Errorf("invalid %s ID token accepted", mode)
		}
	}
	state = provider.begin(t, manager, "unmapped")
	code, binding, err = manager.FinishOIDC(context.Background(), state, state, "code")
	if err != nil {
		t.Fatal(err)
	}
	_, mapped, err := manager.ExchangeOIDC(code, binding)
	if err != nil || mapped.Role != "viewer" || mapped.ID != user.ID {
		t.Fatalf("unmapped group gained access or identity changed: %+v %v", mapped, err)
	}
}
func TestOIDCRejectsUnsafeDiscoveryAndRedirectConfiguration(t *testing.T) {
	manager, _, _, _ := persistentFixture(t)
	for _, cfg := range []OIDCConfig{
		{Issuer: "http://keycloak.example", ClientID: "id", ClientSecret: "secret", RedirectURL: "https://talosdeck.example/api/auth/oidc/callback", AllowHTTP: true},
		{Issuer: "https://issuer.example", ClientID: "id", ClientSecret: "secret", RedirectURL: "https://talosdeck.example/other"},
		{Issuer: "https://user:secret@issuer.example", ClientID: "id", ClientSecret: "secret", RedirectURL: "https://talosdeck.example/api/auth/oidc/callback"},
	} {
		if err := manager.ConfigureOIDC(context.Background(), cfg); err == nil {
			t.Fatal("unsafe OIDC configuration accepted")
		}
	}
}
