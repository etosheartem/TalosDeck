package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
)

var (
	envSecretOnce     sync.Once
	cachedEnvSecret   string
	envPasswordOnce   sync.Once
	cachedEnvPassword string
)

// Claims defines custom JWT claims including user role.
type Claims struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	UserID   string `json:"uid,omitempty"`
	Version  uint64 `json:"version,omitempty"`
	jwt.RegisteredClaims
}

// AuthManager manages authentication credentials, JWT signing, and token revocation.
type AuthManager struct {
	adminPassword string
	jwtSecret     []byte
	tokenTTL      time.Duration
	leeway        time.Duration
	revokedMu     sync.RWMutex
	revokedTokens map[string]time.Time // key: JTI or token string, value: expiration time
	usersMu       sync.RWMutex
	store         SecretStore
	state         *persistentState
	oidc          *OIDC
}

// NewAuthManager initializes an AuthManager with given password and secret.
// Ephemeral random secrets are generated if none are provided (SEC-01).
func NewAuthManager(adminPassword, jwtSecret string) *AuthManager {
	if adminPassword == "" {
		adminPassword = RandomString(16)
		log.Printf("[SECURITY] No admin password configured; ephemeral credentials are not logged. Configure TALOSDECK_ADMIN_PASSWORD.")
	}
	if jwtSecret == "" {
		jwtSecret = RandomString(32)
		log.Printf("[SECURITY] No JWT secret configured. Generated ephemeral 256-bit JWT secret.")
	}

	return &AuthManager{
		adminPassword: adminPassword,
		jwtSecret:     []byte(jwtSecret),
		tokenTTL:      24 * time.Hour,
		leeway:        1 * time.Minute,
		revokedTokens: make(map[string]time.Time),
	}
}

// NewAuthManagerFromEnv initializes an AuthManager reading from environment variables.
// If variables are unset, process-wide ephemeral credentials are used (SEC-01).
func NewAuthManagerFromEnv() *AuthManager {
	adminPassword := os.Getenv("TALOSDECK_ADMIN_PASSWORD")
	if adminPassword == "" {
		envPasswordOnce.Do(func() {
			cachedEnvPassword = RandomString(16)
			log.Printf("[SECURITY] No TALOSDECK_ADMIN_PASSWORD set; configure credentials explicitly.")
		})
		adminPassword = cachedEnvPassword
	}

	jwtSecret := os.Getenv("TALOSDECK_JWT_SECRET")
	if jwtSecret == "" {
		envSecretOnce.Do(func() {
			cachedEnvSecret = RandomString(32)
			log.Printf("[SECURITY] No TALOSDECK_JWT_SECRET set. Generated process-wide ephemeral 256-bit JWT secret.")
		})
		jwtSecret = cachedEnvSecret
	}

	return NewAuthManager(adminPassword, jwtSecret)
}

// SetTokenTTL updates token lifetime (primarily for testing).
func (a *AuthManager) SetTokenTTL(d time.Duration) {
	a.tokenTTL = d
}

// SetLeeway updates clock skew leeway (primarily for testing).
func (a *AuthManager) SetLeeway(d time.Duration) {
	a.leeway = d
}

// VerifyPassword checks if the provided password matches the configured admin password in constant time (SEC-03).
// Supports both bcrypt hashed passwords and constant-time string comparison.
func (a *AuthManager) VerifyPassword(password string) bool {
	if a.Persistent() {
		_, err := a.verifyLocal("admin", password)
		return err == nil
	}
	if password == "" || a.adminPassword == "" {
		return false
	}
	if strings.HasPrefix(a.adminPassword, "$2a$") || strings.HasPrefix(a.adminPassword, "$2b$") {
		return bcrypt.CompareHashAndPassword([]byte(a.adminPassword), []byte(password)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(a.adminPassword)) == 1
}

// GenerateToken generates a signed JWT token valid for tokenTTL with JTI, Issuer, and Subject (SEC-07).
func (a *AuthManager) GenerateToken(username, role string) (string, error) {
	if username == "" {
		username = "admin"
	}
	if role == "" {
		role = "admin"
	}

	now := time.Now().UTC()
	claims := Claims{
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        uuid.New().String(),
			ExpiresAt: jwt.NewNumericDate(now.Add(a.tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-10 * time.Second)),
			Issuer:    "TalosDeck",
			Subject:   username,
		},
	}
	if a.Persistent() {
		a.usersMu.RLock()
		user := a.findUsernameLocked(username)
		if user == nil || user.Disabled || user.Role != role {
			a.usersMu.RUnlock()
			return "", ErrInvalidCredentials
		}
		claims.UserID = user.ID
		claims.Version = user.Version
		a.usersMu.RUnlock()
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(a.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// ValidateToken parses and validates the given JWT token string, enforcing issuer, subject, leeway, and revocation status (SEC-07, SEC-08).
func (a *AuthManager) ValidateToken(tokenString string) (*Claims, error) {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return nil, ErrInvalidToken
	}

	parseOpts := []jwt.ParserOption{
		jwt.WithIssuer("TalosDeck"),
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
	}
	if a.leeway > 0 {
		parseOpts = append(parseOpts, jwt.WithLeeway(a.leeway))
	}

	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.jwtSecret, nil
	}, parseOpts...)

	if err != nil {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	// Validate subject and username (SEC-07)
	if claims.Username == "" || claims.Subject == "" || claims.Subject != claims.Username {
		return nil, ErrInvalidToken
	}

	// Check if token is revoked (SEC-08)
	a.revokedMu.RLock()
	isRevoked := false
	if a.revokedTokens != nil {
		if claims.ID != "" {
			if _, exists := a.revokedTokens[claims.ID]; exists {
				isRevoked = true
			}
		}
		if !isRevoked {
			if _, exists := a.revokedTokens[tokenString]; exists {
				isRevoked = true
			}
		}
	}
	a.revokedMu.RUnlock()

	if isRevoked {
		return nil, ErrInvalidToken
	}
	if a.Persistent() {
		a.usersMu.RLock()
		user := a.state.Users[claims.UserID]
		revoked := a.state.Revoked[claims.ID]
		valid := user != nil && !user.Disabled && user.Username == claims.Username && user.Role == claims.Role && user.Version == claims.Version && claims.ID != "" && revoked.IsZero()
		a.usersMu.RUnlock()
		if !valid {
			return nil, ErrInvalidToken
		}
	}

	return claims, nil
}

// RevokeToken invalidates the specified token until its expiration (SEC-08, SEC-13).
func (a *AuthManager) RevokeToken(tokenString string) error {
	tokenString = strings.TrimSpace(tokenString)
	if tokenString == "" {
		return ErrInvalidToken
	}

	claims, err := a.ValidateToken(tokenString)
	if err != nil {
		return err
	}
	if a.Persistent() {
		return a.changeState(func(state *persistentState) error { state.Revoked[claims.ID] = claims.ExpiresAt.Time; return nil })
	}

	expiry := time.Now().Add(a.tokenTTL)
	if claims.ExpiresAt != nil {
		expiry = claims.ExpiresAt.Time
	}

	a.revokedMu.Lock()
	defer a.revokedMu.Unlock()

	if a.revokedTokens == nil {
		a.revokedTokens = make(map[string]time.Time)
	}

	// SEC-13: Periodic cleanup of expired tokens to prevent unbounded memory growth
	now := time.Now()
	for k, exp := range a.revokedTokens {
		if now.After(exp) {
			delete(a.revokedTokens, k)
		}
	}

	if claims.ID != "" {
		a.revokedTokens[claims.ID] = expiry
	}
	a.revokedTokens[tokenString] = expiry

	return nil
}

// RequireAuth returns a Fiber middleware that checks for a valid JWT Bearer token.
func RequireAuth(authMgr *AuthManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if authMgr == nil {
			return c.Next()
		}

		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Authentication required: missing Authorization header",
				"code":  "UNAUTHORIZED",
			})
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Invalid Authorization header format. Expected 'Bearer <token>'",
				"code":  "UNAUTHORIZED",
			})
		}

		claims, err := authMgr.ValidateToken(parts[1])
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Authentication failed: invalid or expired token",
				"code":  "UNAUTHORIZED",
			})
		}

		c.Locals("user", claims.Username)
		c.Locals("role", claims.Role)
		c.Locals("claims", claims)
		if !Can(claims.Role, c.Method(), c.Path()) {
			return fiber.ErrForbidden
		}

		return c.Next()
	}
}

// GetContextUser returns username from Fiber context locals or extracts it from Authorization header.
func GetContextUser(c *fiber.Ctx, authMgr *AuthManager) string {
	if u, ok := c.Locals("user").(string); ok && u != "" {
		return u
	}

	authHeader := c.Get("Authorization")
	if authHeader != "" && authMgr != nil {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if claims, err := authMgr.ValidateToken(parts[1]); err == nil && claims.Username != "" {
				return claims.Username
			}
		}
	}

	return "viewer"
}

// isTrustedProxy checks if an IP is a loopback or matches configured trusted proxies (SEC-06).
func isTrustedProxy(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if trustedEnv := os.Getenv("TALOSDECK_TRUSTED_PROXIES"); trustedEnv != "" {
		for _, trusted := range strings.Split(trustedEnv, ",") {
			trusted = strings.TrimSpace(trusted)
			if trusted == "" {
				continue
			}
			if strings.Contains(trusted, "/") {
				if _, ipNet, err := net.ParseCIDR(trusted); err == nil && ipNet.Contains(ip) {
					return true
				}
			} else if trusted == ipStr {
				return true
			}
		}
	}
	return false
}

// IsSecureRequest trusts a forwarded transport only from the actual socket peer
// already allowed by the application's proxy policy.
func IsSecureRequest(c *fiber.Ctx) bool {
	return c.Context().IsTLS() || (isTrustedProxy(c.Context().RemoteIP().String()) && strings.EqualFold(strings.TrimSpace(c.Get("X-Forwarded-Proto")), "https"))
}

// GetClientIP returns the real client IP address checking X-Forwarded-For ONLY if direct peer is trusted (SEC-06).
func GetClientIP(c *fiber.Ctx) string {
	directIP := c.IP()
	if isTrustedProxy(directIP) {
		if xff := c.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			clientIP := strings.TrimSpace(parts[0])
			if parsed := net.ParseIP(clientIP); parsed != nil {
				return clientIP
			}
		}
		if xrip := c.Get("X-Real-IP"); xrip != "" {
			clientIP := strings.TrimSpace(xrip)
			if parsed := net.ParseIP(clientIP); parsed != nil {
				return clientIP
			}
		}
	}
	return directIP
}

// RandomString generates a cryptographically secure random hex string for tokens/secrets.
func RandomString(n int) string {
	bytes := make([]byte, n)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
