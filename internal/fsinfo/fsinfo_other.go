//go:build !linux && !darwin

package fsinfo

// detect cannot tell a network file system here.
func detect(string) (Info, error) { return Info{Type: "unknown"}, nil }
