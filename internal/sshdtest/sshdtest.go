// Package sshdtest starts a throwaway sshd on a free loopback port for
// integration tests of rsync over SSH. Tests skip themselves when sshd,
// ssh, rsync or ssh-keygen are not installed or sshd cannot start.
package sshdtest

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// SSHD is a running sshd. It accepts the current user with the keys in
// AuthorizedKeys and stops when the test ends.
type SSHD struct {
	Port           int
	User           string
	AuthorizedKeys string
	log            syncBuffer
}

// Find returns the path of sshd, or "".
func Find() string {
	if p, err := exec.LookPath("sshd"); err == nil {
		return p
	}
	for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd", "/opt/homebrew/sbin/sshd"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// Start runs sshd with authorizedKeys as the current user's
// AuthorizedKeysFile (the file may be written later).
func Start(t *testing.T, authorizedKeys string) *SSHD {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test; skipped with -short")
	}
	for _, bin := range []string{"rsync", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	sshd := Find()
	if sshd == "" {
		t.Skip("sshd not installed")
	}
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user:", err)
	}
	dir := t.TempDir()
	hostKey := filepath.Join(dir, "host_ed25519")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	s := &SSHD{User: u.Username, AuthorizedKeys: authorizedKeys}
	for attempt := 0; attempt < 3; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip("no loopback:", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		cfg := filepath.Join(dir, "sshd_config")
		os.WriteFile(cfg, []byte(fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
AuthorizedKeysFile %s
PidFile %s
AllowUsers %s
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
PermitTTY no
AllowTcpForwarding no
X11Forwarding no
LogLevel VERBOSE
`, port, hostKey, authorizedKeys, filepath.Join(dir, "sshd.pid"), u.Username)), 0o600)
		ctx, cancel := context.WithCancel(context.Background())
		cmd := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", cfg)
		cmd.Stdout, cmd.Stderr = &s.log, &s.log
		if err := cmd.Start(); err != nil {
			cancel()
			t.Skip("cannot start sshd:", err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		t.Cleanup(func() {
			cancel()
			<-exited
			if t.Failed() {
				t.Logf("sshd log:\n%s", s.log.String())
			}
		})
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			select {
			case <-exited:
				deadline = time.Time{}
				continue
			default:
			}
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if err == nil {
				banner, _ := bufio.NewReader(c).ReadString('\n')
				c.Close()
				if strings.HasPrefix(banner, "SSH-") {
					s.Port = port
					return s
				}
			}
		}
		cancel()
		<-exited
		t.Logf("sshd attempt %d did not come up:\n%s", attempt+1, s.log.String())
	}
	t.Skip("sshd does not start in this environment")
	return nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
