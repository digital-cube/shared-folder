//go:build !unix

package authkeys

// lockFile is a no-op where flock is not available.
func lockFile(string) (func(), error) { return func() {}, nil }
