// Package version holds the build version shared by shfd and shf.
package version

// Version is overridden at build time:
//
//	go build -ldflags "-X github.com/digital-cube/shared-folder/internal/version.Version=v1.2.3"
var Version = "dev"
