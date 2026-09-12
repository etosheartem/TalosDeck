package alertcenter

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookSignatureAndNoRedirect(t *testing.T) {
	hit := false
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer receiver.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte("private-signing-key"))
		mac.Write([]byte(r.Header.Get("X-TalosDeck-Timestamp") + "."))
		mac.Write(b)
		if r.Header.Get("X-TalosDeck-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("signature mismatch")
		}
		if r.Header.Get("X-TalosDeck-Event-ID") != "event-1" {
			t.Error("missing event ID")
		}
		w.Header().Set("Location", receiver.URL)
		w.WriteHeader(302)
	}))
	defer server.Close()
	_, err := NewTransport().Send(context.Background(), ChannelSecret{Type: "webhook", Config: map[string]string{"url": server.URL, "allowHTTP": "true", "hmacSecret": "private-signing-key"}}, Notification{ID: "event-1"})
	if err == nil || hit {
		t.Fatal("redirect followed or accepted")
	}
}
func TestTransportMasksErrorsAndBoundsRetry(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "999999")
		w.WriteHeader(429)
		fmt.Fprint(w, "private-token-url")
	}))
	defer s.Close()
	out, err := NewTransport().Send(context.Background(), ChannelSecret{Type: "slack", Config: map[string]string{"url": s.URL + "/private-token-url", "allowHTTP": "true"}}, Notification{})
	if err == nil || strings.Contains(err.Error(), "private-token") || out.RetryAfter != time.Hour {
		t.Fatal("unsafe response handling")
	}
}
func TestDiscordDisablesMentions(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"allowed_mentions":{"parse":[]}`) {
			t.Error("mentions enabled")
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	_, err := NewTransport().Send(context.Background(), ChannelSecret{Type: "discord", Config: map[string]string{"url": s.URL, "allowHTTP": "true"}}, Notification{Title: "@everyone"})
	if err != nil {
		t.Fatal(err)
	}
}
func TestChannelValidation(t *testing.T) {
	for _, c := range []ChannelSecret{{Type: "webhook", Config: map[string]string{"url": "http://example.com"}}, {Type: "webhook", Config: map[string]string{"url": "https://secret@example.com"}}, {Type: "telegram", Config: map[string]string{"token": "x/steal", "chatId": "1"}}, {Type: "email", Config: map[string]string{"smtpHost": "example.com", "smtpPort": "25", "smtpTLS": "none", "smtpFrom": "a@example.com", "smtpTo": "b@example.com"}}} {
		if ValidateChannelConfig(c.Type, c.Config) == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}
func TestSMTPRequiresSTARTTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		fmt.Fprint(c, "220 mock SMTP\r\n")
		reader := bufio.NewReader(c)
		line, _ := reader.ReadString('\n')
		if !strings.HasPrefix(line, "EHLO ") {
			return
		}
		fmt.Fprint(c, "250 mock\r\n")
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_, err = NewTransport().Send(context.Background(), ChannelSecret{Type: "email", Config: map[string]string{"smtpHost": host, "smtpPort": port, "smtpTLS": "starttls", "smtpFrom": "a@example.com", "smtpTo": "b@example.com"}}, Notification{})
	if err == nil {
		t.Fatal("plaintext SMTP accepted")
	}
	<-done
}

func TestSMTPTLSDelivery(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer fixture.Close()
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	ln, err := tls.Listen("tcp", "127.0.0.1:0", fixture.TLS)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	messages := make(chan string, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprint(c, "220 mock SMTP\r\n")
		rd := bufio.NewReader(c)
		for {
			line, e := rd.ReadString('\n')
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				fmt.Fprint(c, "250 mock\r\n")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				fmt.Fprint(c, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(c, "354 go\r\n")
				var b strings.Builder
				for {
					line, e = rd.ReadString('\n')
					if e != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					b.WriteString(line)
				}
				messages <- b.String()
				fmt.Fprint(c, "250 delivered\r\n")
			case strings.HasPrefix(line, "QUIT"):
				fmt.Fprint(c, "221 bye\r\n")
				return
			}
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	tr := NewTransport()
	tr.TLSConfig = &tls.Config{RootCAs: roots}
	_, err = tr.Send(context.Background(), ChannelSecret{Type: "email", Config: map[string]string{"smtpHost": host, "smtpPort": port, "smtpTLS": "tls", "smtpFrom": "a@example.com", "smtpTo": "b@example.com"}}, Notification{Title: "safe\r\nBcc: attacker@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-messages:
		parts := strings.SplitN(message, "\r\n\r\n", 2)
		if len(parts) != 2 || strings.Contains(parts[0], "attacker") {
			t.Fatal("header injection")
		}
		decoded, e := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
		if e != nil || !strings.Contains(string(decoded), "Bcc:") {
			t.Fatal("body encoding")
		}
	case <-time.After(time.Second):
		t.Fatal("missing message")
	}
}
func TestTransportCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	out, err := NewTransport().Send(ctx, ChannelSecret{Type: "webhook", Config: map[string]string{"url": s.URL, "allowHTTP": "true"}}, Notification{})
	if err == nil || !out.Uncertain {
		t.Fatal("ambiguous timeout not classified")
	}
}

func TestWebhookTLSVerification(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer s.Close()
	_, err := NewTransport().Send(context.Background(), ChannelSecret{Type: "webhook", Config: map[string]string{"url": s.URL}}, Notification{})
	if err == nil {
		t.Fatal("untrusted TLS receiver accepted")
	}
}
func TestTelegramRequiresPositiveConfirmation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"description":"private-receiver-secret"}`)
	}))
	defer s.Close()
	tr := NewTransport()
	tr.TelegramBaseURL = s.URL
	out, err := tr.Send(context.Background(), ChannelSecret{Type: "telegram", Config: map[string]string{"token": "1:private-token", "chatId": "1"}}, Notification{})
	if err == nil || !out.Uncertain || strings.Contains(err.Error(), "private") {
		t.Fatal("false confirmation accepted or leaked")
	}
}
