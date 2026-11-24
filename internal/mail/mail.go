// Package mail sends shfd's mails (docs/admin.md, "Zaboravljena lozinka"):
// the link of a forgotten password and the notice that a password was
// changed. Sender is what the server calls; Brevo sends through Brevo's
// transactional API, Fake keeps the messages for tests.
package mail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"sync"
)

// DefaultFrom is the sender when none is configured (SHFD_MAIL_FROM). The
// domain is authenticated in the Brevo account.
const DefaultFrom = "shared-storage <noreply@digitalcube.rs>"

// Address is a mailbox: an address and an optional display name.
type Address struct {
	Name  string
	Email string
}

func (a Address) String() string {
	return (&mail.Address{Name: a.Name, Address: a.Email}).String()
}

// ParseAddress reads "Name <addr@host>" or a bare "addr@host".
func ParseAddress(s string) (Address, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return Address{}, fmt.Errorf("mail address %q: %v", s, err)
	}
	return Address{Name: a.Name, Email: a.Address}, nil
}

// Message is one mail, with an HTML and a plain-text body.
type Message struct {
	To      Address
	Subject string
	Text    string
	HTML    string
}

// Sender sends a message. Its errors never carry a credential.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Fake is a Sender that keeps what it is given. Err, when set, is returned
// instead.
type Fake struct {
	mu   sync.Mutex
	sent []Message
	Err  error
}

// Send records m.
func (f *Fake) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.sent = append(f.sent, m)
	return nil
}

// Sent returns a copy of the messages sent so far.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.sent...)
}

// Reset forgets the messages sent so far.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = nil
}

// ErrNoRecipient is a message without an address to send it to.
var ErrNoRecipient = errors.New("mail: no recipient")
