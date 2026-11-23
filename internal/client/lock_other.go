//go:build !unix

package client

import "errors"

// ErrBusy means another cycle holds the folder.
var ErrBusy = errors.New("another shf is synchronizing this folder")

// lock is a no-op where flock is not available.
func lock(string) (func(), error) { return func() {}, nil }
