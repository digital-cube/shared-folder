package rsyncssh

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/transport"
)

const testShare = "690a2702-1bb6-4866-b49f-cc8bee54f08e"

func script(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseConfig(t *testing.T) Config {
	key := filepath.Join(t.TempDir(), "id")
	os.WriteFile(key, []byte("k"), 0o600)
	return Config{ShareID: testShare, Host: "shf.example.org", User: "shfd", KeyFile: key, StateDir: t.TempDir(),
		SSH: script(t, "exit 0")}
}

func TestNewChecksTools(t *testing.T) {
	for name, tc := range map[string]struct {
		rsync string
		ok    bool
	}{
		"missing":   {"/nonexistent/rsync", false},
		"openrsync": {script(t, `echo "openrsync: protocol version 29"`), false},
		"too old":   {script(t, `echo "rsync  version 3.1.3  protocol version 31"`), false},
		"3.2.4":     {script(t, `echo "rsync  version 3.2.4  protocol version 31"`), true},
		"3.4.1":     {script(t, `echo "rsync  version v3.4.1  protocol version 32"`), true},
	} {
		cfg := baseConfig(t)
		cfg.Rsync = tc.rsync
		_, err := New(cfg)
		if tc.ok != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
		if !tc.ok && !errors.Is(err, ErrNotInstalled) {
			t.Errorf("%s: not ErrNotInstalled: %v", name, err)
		}
	}
	cfg := baseConfig(t)
	cfg.Rsync, cfg.SSH = script(t, `echo "rsync  version 3.2.7"`), "/nonexistent/ssh"
	if _, err := New(cfg); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "ssh not found") {
		t.Errorf("missing ssh: %v", err)
	}
}

func TestRsyncMessageOnMacOS(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	for name, tc := range map[string]struct {
		goos, out string
		want      []string
	}{
		"openrsync on macOS": {"darwin", "openrsync: protocol version 29\nrsync version 2.6.9 compatible",
			[]string{"Apple's openrsync", "brew install rsync", "brew shellenv", "ahead of /usr/bin", "shf service install"}},
		"rsync 2.6.9 on macOS": {"darwin", "rsync  version 2.6.9  protocol version 29",
			[]string{"Apple's rsync 2.6.9", "brew install rsync", "brew shellenv"}},
		"openrsync elsewhere": {"linux", "openrsync: protocol version 29\nrsync version 2.6.9 compatible",
			[]string{"is not the rsync shf needs", "3.2.4"}},
		"old rsync on Linux": {"linux", "rsync  version 3.1.3  protocol version 31",
			[]string{"rsync 3.1.3 is too old", "3.2.4"}},
	} {
		goos = tc.goos
		bin := script(t, "cat <<'EOF'\n"+tc.out+"\nEOF")
		_, err := FindTools(bin, script(t, "exit 0"))
		if !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("%s: %v", name, err)
		}
		for _, w := range append(tc.want, bin) {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: message lacks %q: %v", name, w, err)
			}
		}
	}
}

func TestFindToolsFromEnvironment(t *testing.T) {
	rs, ssh := script(t, `echo "rsync  version 3.2.7"`), script(t, "exit 0")
	t.Setenv(EnvRsync, rs)
	t.Setenv(EnvSSH, ssh)
	tools, err := FindTools("", "")
	if err != nil || tools.Rsync != rs || tools.SSH != ssh {
		t.Fatalf("FindTools: %+v %v", tools, err)
	}
	// the variables are checked the same way as Config
	t.Setenv(EnvRsync, script(t, `echo "rsync  version 3.1.3"`))
	if _, err := FindTools("", ""); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("old rsync from %s: %v", EnvRsync, err)
	}
	cfg := baseConfig(t)
	cfg.SSH = ""
	if _, err := New(cfg); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("New with old $%s: %v", EnvRsync, err)
	}
	// a path given in Config wins over the variable
	cfg.Rsync = rs
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestNewValidates(t *testing.T) {
	rs := script(t, `echo "rsync  version 3.2.7"`)
	for name, mut := range map[string]func(*Config){
		"share":       func(c *Config) { c.ShareID = "x" },
		"host option": func(c *Config) { c.Host = "-oProxyCommand=x" },
		"host user":   func(c *Config) { c.Host = "a@b" },
		"user":        func(c *Config) { c.User = "a b" },
		"port":        func(c *Config) { c.Port = 70000 },
		"no key":      func(c *Config) { c.KeyFile = filepath.Join(t.TempDir(), "none") },
		"quote":       func(c *Config) { c.StateDir = filepath.Join(t.TempDir(), `a"b`) },
	} {
		cfg := baseConfig(t)
		cfg.Rsync = rs
		mut(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRshQuoteRoundTrip(t *testing.T) {
	// rsync splits -e on blanks; inside '…' a doubled quote is one quote.
	for in, want := range map[string]string{
		"/usr/bin/ssh":              "/usr/bin/ssh",
		"/Users/a/Library/App Supp": "'/Users/a/Library/App Supp'",
		"it's":                      "'it''s'",
		`UserKnownHostsFile="/a b"`: `'UserKnownHostsFile="/a b"'`,
		"":                          "''",
	} {
		if got := rshQuote(in); got != want {
			t.Errorf("rshQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	exit := &exec.ExitError{}
	for stderr, want := range map[string]error{
		"shfd rsync-gate: denied (not_found): storage/x does not exist\nrsync: connection unexpectedly closed": transport.ErrChanged,
		"shfd rsync-gate: denied (upload): abc is not an open upload of ana\n":                                 ErrDenied,
		"shfd rsync-gate: denied (member): member ana is revoked\n":                                            ErrAuth,
		"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\nHost key verification failed.\n":            ErrHostKey,
		"igor@127.0.0.1: Permission denied (publickey).\n":                                                     ErrAuth,
		"ssh: connect to host x port 22: Connection refused\n":                                                 ErrUnreachable,
		"ssh: Could not resolve hostname nope: Name or service not known\n":                                    ErrUnreachable,
		"[sender] io timeout after 60 seconds -- exiting\n":                                                    ErrUnreachable,
	} {
		if err := classify("storage/x", exit, stderr); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", stderr, err, want)
		}
	}
}

func TestParseStats(t *testing.T) {
	out := `
Number of files: 1 (reg: 1)
Total file size: 50,331,648 bytes
Total transferred file size: 50,331,648 bytes
Literal data: 43,221,432 bytes
Matched data: 7,110,216 bytes
`
	st := parseStats(out)
	if st.Size != 50331648 || st.Literal != 43221432 || st.Matched != 7110216 {
		t.Errorf("%+v", st)
	}
}

func TestCleanEnv(t *testing.T) {
	got := cleanEnv([]string{"HOME=/h", "RSYNC_PROTECT_ARGS=1", "RSYNC_OLD_ARGS=1", "LC_ALL=sr_RS.UTF-8"})
	if strings.Join(got, " ") != "HOME=/h LC_ALL=C" {
		t.Errorf("%q", got)
	}
}

func TestControlPath(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id")
	os.WriteFile(key, []byte("key"), 0o600)
	cfg := Config{Host: "h", Port: 22, User: "shf", KeyFile: key, StateDir: "/s/abc"}
	p := controlPath(cfg)
	if filepath.Dir(p) != cfg.StateDir || len(p) > maxControlPath {
		t.Errorf("short state dir: %s", p)
	}
	if q := controlPath(cfg); q != p {
		t.Errorf("not stable: %s, %s", p, q)
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Host = "other" },
		func(c *Config) { c.Port = 2222 },
		func(c *Config) { c.User = "x" },
		func(c *Config) { os.WriteFile(c.KeyFile, []byte("new key"), 0o600) },
	} {
		c := cfg
		change(&c)
		if q := controlPath(c); q == p {
			t.Errorf("same control path for another connection: %s", q)
		}
	}

	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	cfg.StateDir = "/" + strings.Repeat("long/", 20) + "state"
	if p := controlPath(cfg); !strings.HasPrefix(p, "/run/user/1000/shf/") || len(p) > maxControlPath {
		t.Errorf("long state dir: %s", p)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if p := controlPath(cfg); !strings.HasPrefix(p, "/tmp/shf-") || len(p) > maxControlPath {
		t.Errorf("long state dir, no XDG_RUNTIME_DIR: %s", p)
	}
}
