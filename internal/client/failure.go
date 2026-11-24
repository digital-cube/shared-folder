package client

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// Failure says what kind of failure ended a cycle, as far as retrying it
// is concerned.
type Failure int

const (
	// FailOther is any other failure (or none).
	FailOther Failure = iota
	// FailUnreachable: the server could not be reached (SSH or HTTP), or
	// ssh failed in a way rsync reports with exit status 255. It passes by
	// itself; retry with a growing pause.
	FailUnreachable
	// FailKeyRefused: sshd (or rsync-gate) refused this device's SSH key.
	// It does not pass by itself: the member was revoked, or the same
	// member joined from another device and that device's key replaced
	// this one. Every retry is another refused login at the server's sshd.
	FailKeyRefused
)

// ErrKeyRefused is in the chain of a cycle's error when the server
// refused the SSH key (errors.Is). rsyncssh.ErrAuth stays in it too.
var ErrKeyRefused = errors.New(KeyRefusedHint)

// KeyRefusedHint is what the user is told when the server refuses the
// device's SSH key.
const KeyRefusedHint = "the server does not accept this device's SSH key; if the same member joined from " +
	"another device, that replaced this device's key: run shf join again here with your own token"

// Classify tells how err should be retried.
func Classify(err error) Failure {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return FailOther
	case errors.Is(err, rsyncssh.ErrAuth), errors.Is(err, ErrKeyRefused):
		return FailKeyRefused
	case errors.Is(err, rsyncssh.ErrUnreachable):
		return FailUnreachable
	}
	var ne net.Error
	var ae *APIError
	if !errors.As(err, &ae) && errors.As(err, &ne) {
		return FailUnreachable // the HTTP API: dial, TLS or timeout
	}
	return FailOther
}

// transportDown reports whether err means no further transfer of this
// cycle can succeed: each would be one more refused or failing SSH login.
func transportDown(err error) bool {
	f := Classify(err)
	return f == FailKeyRefused || f == FailUnreachable
}

// explain adds KeyRefusedHint to an error that is a refused key.
func explain(err error) error {
	if Classify(err) != FailKeyRefused || errors.Is(err, ErrKeyRefused) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrKeyRefused, err)
}
