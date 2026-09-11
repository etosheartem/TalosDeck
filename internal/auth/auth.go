package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
)

// Claims defines custom JWT claims including user role.
type Claims struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// AuthManager manages authentication credentials and JWT signing.
type AuthManager struct {
	adminPassword string
	jwtSecret     []byte
	tokenTTL      time.Duration
}

// NewAuthManager initializes an AuthManager with given password and secret.
func NewAuthManager(adminPassword, jwtSecret string) *AuthManager {
	if adminPassword == "" {
		adminPassword = "admin"
	}
	if jwtSecret == "" {
		jwtSecret = "talosdeck-default-secret-key-32-chars-long-jwt-auth"
	}

	return &AuthManager{
		adminPassword: adminPassword,
		jwtSecret:     []byte(jwtSecret),
		tokenTTL:      24 * time.Hour,
	}
}

// NewAuthManagerFromEnv initializes an AuthManager reading from environment variables.
func NewAuthManagerFromEnv() *AuthManager {
	adminPassword := os.Getenv("TALOSDECK_ADMIN_PASSWORD")
	jwtSecret := os.Getenv("TALOSDECK_JWT_SECRET")
	return NewAuthManager(adminPassword, jwtSecret)
}

// VerifyPassword checks if the provided password matches the configured admin password.
func (a *AuthManager) VerifyPassword(password string) bool {
	return password == a.adminPassword
}

// GenerateToken generates a signed JWT token valid for 24 hours.
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
			ExpiresAt: jwt.NewNumericDate(now.Add(a.tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "TalosDeck",
			Subject:   username,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(a.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// ValidateToken parses and validates the given JWT token string.
func (a *AuthManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.jwtSecret, nil
	})

	if err != nil {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// RequireAuth returns a Fiber middleware that checks for a valid JWT Bearer token.
func RequireAuth(authMgr *AuthManager) fiber.Handler {
	return func(c *fiber.Ctx) error {
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

// GetClientIP returns the real client IP address checking X-Forwarded-For first.
func GetClientIP(c *fiber.Ctx) string {
	xff := c.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	return c.IP()
}

// RandomString generates a random hex string for tokens/secrets.
func RandomString(n int) string {
	bytes := make([]byte, n)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
