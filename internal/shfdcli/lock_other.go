//go:build !unix

package shfdcli

// lockDataRoot is a no-op where flock is not available.
func lockDataRoot(string) (func(), error) { return func() {}, nil }
