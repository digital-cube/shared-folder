// Package e2e holds the end-to-end tests of the shared folder service: the
// real shfd binary, two or three real shf clients in daemon mode, files
// moving through real rsync over a throwaway sshd. The tests are behind the
// build tag e2e and run with "make e2e" (not with "make check"); see
// README.md for what the machine needs.
package e2e
