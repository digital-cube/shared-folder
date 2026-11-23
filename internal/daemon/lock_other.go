//go:build !unix

package daemon

// lockHome is a no-op where flock is not available.
func lockHome(string) (func(), error) { return func() {}, nil }
