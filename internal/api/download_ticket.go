package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
	"talosdeck/internal/auth"
)

const downloadCookie = "talosdeck_backup_download"
const downloadTicketLimit = 256

func isBackupDownloadPath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 4 {
		return parts[0] == "api" && parts[1] == "backups" && parts[2] != "" && parts[3] == "download"
	}
	return len(parts) == 6 && parts[0] == "api" && parts[1] == "clusters" && parts[2] != "" && parts[3] == "backups" && parts[4] != "" && parts[5] == "download"
}

func backupRequestConfig(header *fasthttp.RequestHeader) fasthttp.RequestConfig {
	path, _, _ := strings.Cut(string(header.RequestURI()), "?")
	if string(header.Method()) == fiber.MethodGet && isBackupDownloadPath(path) {
		return fasthttp.RequestConfig{WriteTimeout: 30 * time.Minute}
	}
	return fasthttp.RequestConfig{}
}

type downloadTicket struct {
	path, token string
	expires     time.Time
	secure      bool
}

// DownloadTickets exchanges an authenticated request for one native browser
// download. Neither the session JWT nor a capability is placed in a URL.
type DownloadTickets struct {
	mu      sync.Mutex
	auth    *auth.AuthManager
	tickets map[[32]byte]downloadTicket
	now     func() time.Time
}

func NewDownloadTickets(am *auth.AuthManager) *DownloadTickets {
	return &DownloadTickets{auth: am, tickets: make(map[[32]byte]downloadTicket), now: time.Now}
}

func (d *DownloadTickets) Issue(c *fiber.Ctx, clusterID, backupID string) error {
	if backupID == "" || strings.ContainsAny(backupID, "/\\\x00") || backupID == "." || backupID == ".." {
		return fiber.ErrBadRequest
	}
	parts := strings.SplitN(c.Get(fiber.HeaderAuthorization), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return fiber.ErrUnauthorized
	}
	claims, err := d.auth.ValidateToken(parts[1])
	if err != nil {
		return fiber.ErrUnauthorized
	}
	if claims.Role != "admin" {
		return fiber.ErrForbidden
	}
	path := "/api/backups/" + url.PathEscape(backupID) + "/download"
	if clusterID != "" {
		path = "/api/clusters/" + url.PathEscape(clusterID) + "/backups/" + url.PathEscape(backupID) + "/download"
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return fiber.ErrInternalServerError
	}
	value := base64.RawURLEncoding.EncodeToString(nonce[:])
	key := sha256.Sum256([]byte(value))
	now := d.now()
	secure := auth.IsSecureRequest(c)
	d.mu.Lock()
	for k, ticket := range d.tickets {
		if !now.Before(ticket.expires) {
			delete(d.tickets, k)
		}
	}
	if len(d.tickets) >= downloadTicketLimit {
		d.mu.Unlock()
		return fiber.NewError(429, "Too many pending downloads; retry after one minute")
	}
	d.tickets[key] = downloadTicket{path: strings.Clone(path), token: strings.Clone(parts[1]), expires: now.Add(time.Minute), secure: secure}
	d.mu.Unlock()
	c.Cookie(&fiber.Cookie{Name: downloadCookie, Value: value, Path: path, HTTPOnly: true, Secure: secure, SameSite: "Strict", MaxAge: 60, Expires: now.Add(time.Minute)})
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(fiber.Map{"ready": true})
}

// Authenticate must run before fleet dispatch, while the original scoped path
// is intact. The usual authentication and role checks still run afterwards.
func (d *DownloadTickets) Authenticate(c *fiber.Ctx) error {
	if c.Method() != fiber.MethodGet || c.Get(fiber.HeaderAuthorization) != "" || !isBackupDownloadPath(c.Path()) {
		return c.Next()
	}
	value := c.Cookies(downloadCookie)
	if value == "" {
		return c.Next()
	}
	key := sha256.Sum256([]byte(value))
	now := d.now()
	d.mu.Lock()
	ticket, ok := d.tickets[key]
	if ok && (!now.Before(ticket.expires) || ticket.path == c.Path()) {
		delete(d.tickets, key)
	}
	d.mu.Unlock()
	if !ok || !now.Before(ticket.expires) || ticket.path != c.Path() {
		return fiber.ErrUnauthorized
	}
	c.Cookie(&fiber.Cookie{Name: downloadCookie, Value: "", Path: ticket.path, HTTPOnly: true, Secure: ticket.secure, SameSite: "Strict", MaxAge: -1, Expires: time.Unix(1, 0)})
	claims, err := d.auth.ValidateToken(ticket.token)
	if err != nil {
		return fiber.ErrUnauthorized
	}
	if !auth.Can(claims.Role, c.Method(), c.Path()) {
		return fiber.ErrForbidden
	}
	c.Request().Header.Set(fiber.HeaderAuthorization, "Bearer "+ticket.token)
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Next()
}
