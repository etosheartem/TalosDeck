package alertcenter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Transport has no retries: the durable outbox owns retry and uncertainty policy.
type Transport struct {
	HTTPClient      *http.Client
	TLSConfig       *tls.Config
	TelegramBaseURL string
}

func NewTransport() *Transport {
	return &Transport{HTTPClient: &http.Client{Timeout: 20 * time.Second}, TelegramBaseURL: "https://api.telegram.org"}
}
func ValidateChannelConfig(kind string, c map[string]string) error {
	bad := errors.New("invalid notification channel configuration")
	allowed := map[string]bool{}
	keys := func(s string) {
		for _, k := range strings.Fields(s) {
			allowed[k] = true
		}
	}
	switch kind {
	case "telegram":
		keys("token chatId")
		if c["token"] == "" || c["chatId"] == "" || strings.ContainsAny(c["token"], "/?#@ \r\n\t") {
			return bad
		}
	case "slack", "discord", "webhook":
		keys("url allowHTTP")
		if kind == "webhook" {
			keys("hmacSecret")
		}
		u, e := url.Parse(c["url"])
		if e != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && c["allowHTTP"] == "true")) {
			return bad
		}
		if c["allowHTTP"] != "" && c["allowHTTP"] != "true" && c["allowHTTP"] != "false" {
			return bad
		}
	case "email":
		keys("smtpHost smtpPort smtpUsername smtpPassword smtpFrom smtpTo smtpTLS")
		port, e := strconv.Atoi(c["smtpPort"])
		if e != nil || port < 1 || port > 65535 || c["smtpHost"] == "" || strings.ContainsAny(c["smtpHost"], "/\r\n\t @") || (c["smtpTLS"] != "tls" && c["smtpTLS"] != "starttls") {
			return bad
		}
		if (c["smtpUsername"] == "") != (c["smtpPassword"] == "") {
			return bad
		}
		if _, e = mail.ParseAddress(c["smtpFrom"]); e != nil || strings.ContainsAny(c["smtpFrom"], "\r\n") {
			return bad
		}
		recipients, e := mail.ParseAddressList(c["smtpTo"])
		if e != nil || len(recipients) == 0 || len(recipients) > 20 || strings.ContainsAny(c["smtpTo"], "\r\n") {
			return bad
		}
	default:
		return bad
	}
	for k, v := range c {
		if !allowed[k] || len(v) > 8192 || strings.ContainsRune(v, 0) {
			return bad
		}
	}
	return nil
}
func clipped(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func notificationText(n Notification) string {
	return clipped(fmt.Sprintf("TalosDeck [%s] %s\n%s\nCluster: %s\nState: %s\nEvent: %s", n.Severity, n.Title, n.Details, n.ClusterID, n.State, n.ID), 3000)
}
func (t *Transport) Send(ctx context.Context, c ChannelSecret, n Notification) (DeliveryOutcome, error) {
	if err := ValidateChannelConfig(c.Type, c.Config); err != nil {
		return DeliveryOutcome{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if c.Type == "email" {
		return t.email(ctx, c.Config, n)
	}
	endpoint := c.Config["url"]
	text := notificationText(n)
	var payload any
	switch c.Type {
	case "telegram":
		base := t.TelegramBaseURL
		if base == "" {
			base = "https://api.telegram.org"
		}
		endpoint = base + "/bot" + c.Config["token"] + "/sendMessage"
		payload = map[string]any{"chat_id": c.Config["chatId"], "text": text, "disable_web_page_preview": true}
	case "slack":
		text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
		payload = map[string]any{"text": text, "mrkdwn": false, "link_names": false}
	case "discord":
		payload = map[string]any{"content": clipped(text, 1900), "allowed_mentions": map[string]any{"parse": []string{}}}
	case "webhook":
		payload = map[string]any{"eventId": n.ID, "clusterId": n.ClusterID, "alertId": n.AlertID, "state": n.State, "severity": n.Severity, "title": clipped(n.Title, 512), "details": clipped(n.Details, 4096), "node": n.Node, "ruleId": n.RuleID, "occurredAt": n.OccurredAt}
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > 16384 {
		return DeliveryOutcome{}, errors.New("notification payload invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return DeliveryOutcome{}, errors.New("notification request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Type == "webhook" {
		req.Header.Set("X-TalosDeck-Event-ID", n.ID)
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-TalosDeck-Timestamp", stamp)
		if secret := c.Config["hmacSecret"]; secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(stamp + "."))
			mac.Write(body)
			req.Header.Set("X-TalosDeck-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
	}
	client := http.Client{Timeout: 20 * time.Second}
	if t.HTTPClient != nil {
		client = *t.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return DeliveryOutcome{Uncertain: true}, errors.New("notification transport failed")
	}
	defer res.Body.Close()
	response, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(response) > 65536 {
		return DeliveryOutcome{Uncertain: true}, errors.New("notification response unavailable")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		out := DeliveryOutcome{}
		if res.StatusCode == 429 {
			if s, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && s > 0 {
				if s > 3600 {
					s = 3600
				}
				out.RetryAfter = time.Duration(s) * time.Second
			}
		}
		return out, errors.New("notification receiver rejected request")
	}
	if c.Type == "telegram" {
		var result struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(response, &result) != nil || !result.OK {
			return DeliveryOutcome{Uncertain: true}, errors.New("notification receiver confirmation missing")
		}
	}
	return DeliveryOutcome{}, nil
}
func (t *Transport) email(ctx context.Context, c map[string]string, n Notification) (out DeliveryOutcome, err error) {
	// Never return SMTP server text: it can echo recipients, credentials or message data.
	defer func() {
		if err != nil {
			err = errors.New("notification SMTP delivery failed")
		}
	}()
	host := c["smtpHost"]
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, c["smtpPort"]))
	if err != nil {
		return out, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if t.TLSConfig != nil {
		cfg = t.TLSConfig.Clone()
		cfg.ServerName = host
		cfg.InsecureSkipVerify = false
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
	}
	if c["smtpTLS"] == "tls" {
		secured := tls.Client(conn, cfg)
		if err = secured.HandshakeContext(ctx); err != nil {
			return out, err
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return out, err
	}
	defer client.Close()
	if c["smtpTLS"] == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return out, errors.New("TLS required")
		}
		if err = client.StartTLS(cfg); err != nil {
			return out, err
		}
	}
	if c["smtpUsername"] != "" {
		if err = client.Auth(smtp.PlainAuth("", c["smtpUsername"], c["smtpPassword"], host)); err != nil {
			return out, err
		}
	}
	from, _ := mail.ParseAddress(c["smtpFrom"])
	recipients, _ := mail.ParseAddressList(c["smtpTo"])
	if err = client.Mail(from.Address); err != nil {
		return out, err
	}
	for _, r := range recipients {
		if err = client.Rcpt(r.Address); err != nil {
			return out, err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return out, err
	}
	out.Uncertain = true
	// Fixed headers prevent subject/event/header injection. No attachments or HTML.
	body := "From: " + from.String() + "\r\nTo: undisclosed-recipients:;\r\nSubject: TalosDeck notification\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"
	encoded := encodeMailBody(notificationText(n))
	_, err = io.WriteString(writer, body+encoded)
	if err != nil {
		return out, err
	}
	if err = writer.Close(); err != nil {
		return out, err
	}
	out.Uncertain = false
	_ = client.Quit()
	return out, nil
}

func encodeMailBody(s string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
	return b.String()
}
