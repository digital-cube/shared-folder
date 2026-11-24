package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

// BrevoURL is Brevo's endpoint for a transactional mail.
const BrevoURL = "https://api.brevo.com/v3/smtp/email"

// Brevo sends through Brevo's transactional API
// (POST /v3/smtp/email with the header api-key). The key is read from a
// file and is never part of a log line, an error or a String: the type
// prints as brevo(from=…).
type Brevo struct {
	key    string
	from   Address
	url    string
	client *http.Client
}

// ErrEmptyKey is a key file with nothing in it.
var ErrEmptyKey = errors.New("brevo: the key file is empty")

// LoadKey reads the API key from a file: its first line, trimmed. It says
// nothing of the content in an error. worldReadable reports a file that
// any user of the machine may read (it should be root:shfd 0640).
func LoadKey(path string) (key string, worldReadable bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("brevo key file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("brevo key file %s: not a regular file", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("brevo key file: %w", err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	key = strings.TrimSpace(line)
	if key == "" {
		return "", false, ErrEmptyKey
	}
	if strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", false, fmt.Errorf("brevo key file %s: the key has spaces or control characters", path)
	}
	return key, fi.Mode().Perm()&fs.FileMode(0o004) != 0, nil
}

// NewBrevo makes a sender with key from From; url "" is BrevoURL (another
// one is for tests: a fake server).
func NewBrevo(key string, from Address, url string) *Brevo {
	if url == "" {
		url = BrevoURL
	}
	return &Brevo{key: key, from: from, url: url, client: &http.Client{Timeout: 15 * time.Second}}
}

// From is the sender of every message.
func (b *Brevo) From() Address { return b.from }

func (b *Brevo) String() string             { return "brevo(from=" + b.from.String() + ")" }
func (b *Brevo) GoString() string           { return b.String() }
func (b *Brevo) LogValue() slog.Value       { return slog.StringValue(b.String()) }
func (b *Brevo) Format(f fmt.State, _ rune) { io.WriteString(f, b.String()) }

type brevoContact struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

type brevoRequest struct {
	Sender      brevoContact   `json:"sender"`
	To          []brevoContact `json:"to"`
	Subject     string         `json:"subject"`
	HTMLContent string         `json:"htmlContent"`
	TextContent string         `json:"textContent"`
}

// maxErrBody bounds what of Brevo's refusal goes into the error.
const maxErrBody = 512

// Send posts the message to Brevo. A refusal is an error with Brevo's
// status and message (with the key, should it ever appear, cut out).
func (b *Brevo) Send(ctx context.Context, m Message) error {
	if m.To.Email == "" {
		return ErrNoRecipient
	}
	body, err := json.Marshal(brevoRequest{
		Sender:      brevoContact{Name: b.from.Name, Email: b.from.Email},
		To:          []brevoContact{{Name: m.To.Name, Email: m.To.Email}},
		Subject:     m.Subject,
		HTMLContent: m.HTML,
		TextContent: m.Text,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return b.scrub(err)
	}
	req.Header.Set("api-key", b.key)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	res, err := b.client.Do(req)
	if err != nil {
		return b.scrub(fmt.Errorf("brevo: %w", err))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxErrBody))
	if res.StatusCode/100 == 2 {
		return nil
	}
	msg := strings.TrimSpace(string(raw))
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		msg = strings.TrimSpace(e.Code + ": " + e.Message)
	}
	return b.scrub(fmt.Errorf("brevo: %s: %s", res.Status, msg))
}

// scrub removes the key from an error's text.
func (b *Brevo) scrub(err error) error {
	if err == nil || b.key == "" || !strings.Contains(err.Error(), b.key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), b.key, "[key]"))
}
