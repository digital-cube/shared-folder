package service_test

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/service"
	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

type recorder struct {
	calls []string
	reply map[string]string // command line prefix -> output
}

func (r *recorder) run(name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, line)
	for k, v := range r.reply {
		if strings.HasPrefix(line, k) {
			return []byte(v), nil
		}
	}
	return nil, nil
}

// tools puts fake rsync and ssh into a directory whose name has a blank
// and an XML special character, and makes it the test's PATH. rsyncOut is
// what the fake rsync --version prints.
func tools(t *testing.T, rsyncOut string) (dir string) {
	dir = filepath.Join(t.TempDir(), "my tools & co")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"rsync": "cat <<'EOF'\n" + rsyncOut + "\nEOF", "ssh": "exit 0"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(filepath.ListSeparator)+"/usr/bin:/bin")
	t.Setenv(rsyncssh.EnvRsync, "")
	t.Setenv(rsyncssh.EnvSSH, "")
	return dir
}

const goodRsync = "rsync  version 3.4.1  protocol version 32"

func env(t *testing.T, goos string, r *recorder) (service.Env, string) {
	home := t.TempDir()
	return service.Env{
		GOOS: goos,
		Getenv: func(k string) string {
			if k == "PATH" {
				return os.Getenv(k)
			}
			return map[string]string{"HOME": home}[k]
		},
		Exe:       "/opt/my apps/shf",
		StateHome: "/data/shf state",
		Run:       r.run,
	}, home
}

func TestSystemd(t *testing.T) {
	r := &recorder{reply: map[string]string{"systemctl --user is-active": "active", "systemctl --user status": "● shf.service - shf shared folder client"}}
	bin := tools(t, goodRsync)
	e, home := env(t, "linux", r)
	m, err := service.For(e)
	if err != nil {
		t.Fatal(err)
	}
	if want := home + "/.config/systemd/user/shf.service"; m.File() != want {
		t.Fatalf("unit at %s, want %s", m.File(), want)
	}
	if _, _, err := m.Status(); !errors.Is(err, service.ErrNotInstalled) {
		t.Fatalf("status before install: %v", err)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(m.File())
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	for _, want := range []string{
		`ExecStart="/opt/my apps/shf" daemon`,
		`Environment="SHF_STATE_HOME=/data/shf state"`,
		`Environment="PATH=` + bin + `:/usr/bin:/bin"`,
		`Environment="SHF_RSYNC=` + bin + `/rsync"`,
		`Environment="SHF_SSH=` + bin + `/ssh"`,
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	wantCalls := []string{"systemctl --user daemon-reload", "systemctl --user enable shf.service", "systemctl --user restart shf.service"}
	if strings.Join(r.calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Errorf("calls:\n%s", strings.Join(r.calls, "\n"))
	}
	report, running, err := m.Status()
	if err != nil || !running || !strings.Contains(report, "shf.service") {
		t.Fatalf("status: %q %v %v", report, running, err)
	}
	r.calls = nil
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.File()); !os.IsNotExist(err) {
		t.Fatalf("unit still there: %v", err)
	}
	if r.calls[0] != "systemctl --user disable --now shf.service" {
		t.Errorf("calls: %v", r.calls)
	}
	if err := m.Uninstall(); !errors.Is(err, service.ErrNotInstalled) {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestLaunchd(t *testing.T) {
	r := &recorder{reply: map[string]string{"launchctl print": "state = running\npid = 42"}}
	bin := tools(t, goodRsync)
	xbin := strings.ReplaceAll(bin, "&", "&amp;")
	e, home := env(t, "darwin", r)
	m, err := service.For(e)
	if err != nil {
		t.Fatal(err)
	}
	if want := home + "/Library/LaunchAgents/rs.digitalcube.shf.plist"; m.File() != want {
		t.Fatalf("agent at %s, want %s", m.File(), want)
	}
	if err := m.Install(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(m.File())
	plist := string(b)
	for _, want := range []string{
		"<string>/opt/my apps/shf</string>",
		"<string>daemon</string>",
		"<key>SHF_STATE_HOME</key>",
		"<string>" + home + "/Library/Logs/shf.log</string>",
		"<key>RunAtLoad</key>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/local/bin:" + xbin + ":/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<key>SHF_RSYNC</key>\n\t\t<string>" + xbin + "/rsync</string>",
		"<key>SHF_SSH</key>\n\t\t<string>" + xbin + "/ssh</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
	}
	// well-formed, and the values read back unescaped
	vars := plistEnv(t, plist)
	if vars["SHF_RSYNC"] != bin+"/rsync" || vars["SHF_SSH"] != bin+"/ssh" || vars["SHF_STATE_HOME"] != "/data/shf state" {
		t.Errorf("EnvironmentVariables: %q", vars)
	}
	if p := vars["PATH"]; !strings.HasPrefix(p, "/opt/homebrew/bin:/usr/local/bin:") || !strings.HasSuffix(p, ":/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Errorf("PATH: %q", p)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if last := r.calls[len(r.calls)-1]; last != "launchctl bootstrap "+domain+" "+m.File() {
		t.Errorf("calls: %v", r.calls)
	}
	if _, running, err := m.Status(); err != nil || !running {
		t.Fatalf("status: %v %v", running, err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.File()); !os.IsNotExist(err) {
		t.Fatal("plist still there")
	}
}

func TestUnsupported(t *testing.T) {
	e, _ := env(t, "windows", &recorder{})
	if _, err := service.For(e); !errors.Is(err, service.ErrUnsupported) {
		t.Fatal(err)
	}
}

// plistEnv parses plist as XML (failing on malformed XML) and returns the
// agent's EnvironmentVariables.
func plistEnv(t *testing.T, plist string) map[string]string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(plist))
	d.Strict = true
	var keys, strs []string
	var path []string
	var text strings.Builder
	var topKey string // the last key of the top-level dict
	vars := map[string]string{}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, plist)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			path = append(path, tok.Name.Local)
			text.Reset()
		case xml.EndElement:
			if len(path) == 3 && path[2] == "key" {
				topKey = text.String()
			}
			// plist/dict/dict/{key,string} inside EnvironmentVariables
			if len(path) == 4 && path[2] == "dict" && topKey == "EnvironmentVariables" {
				switch path[3] {
				case "key":
					keys = append(keys, text.String())
				case "string":
					strs = append(strs, text.String())
				}
			}
			path = path[:len(path)-1]
		case xml.CharData:
			text.Write(tok)
		}
	}
	if len(keys) != len(strs) {
		t.Fatalf("EnvironmentVariables: %d keys, %d strings", len(keys), len(strs))
	}
	for i, k := range keys {
		vars[k] = strs[i]
	}
	return vars
}

func TestInstallRefusesOldRsync(t *testing.T) {
	for name, out := range map[string]string{
		"rsync 3.1.3": "rsync  version 3.1.3  protocol version 31",
		"openrsync":   "openrsync: protocol version 29\nrsync version 2.6.9 compatible",
	} {
		for _, goos := range []string{"linux", "darwin"} {
			tools(t, out)
			r := &recorder{}
			e, _ := env(t, goos, r)
			m, err := service.For(e)
			if err != nil {
				t.Fatal(err)
			}
			err = m.Install()
			if !errors.Is(err, rsyncssh.ErrNotInstalled) || !strings.Contains(err.Error(), "3.2.4") {
				t.Errorf("%s on %s: %v", name, goos, err)
			}
			if _, serr := os.Stat(m.File()); !os.IsNotExist(serr) {
				t.Errorf("%s on %s: %s written anyway", name, goos, m.File())
			}
			if len(r.calls) != 0 {
				t.Errorf("%s on %s: service manager called: %v", name, goos, r.calls)
			}
			if _, err := e.Resolve(); !errors.Is(err, rsyncssh.ErrNotInstalled) {
				t.Errorf("%s on %s: Resolve: %v", name, goos, err)
			}
		}
	}
	// no rsync at all
	dir := tools(t, goodRsync)
	os.Remove(filepath.Join(dir, "rsync"))
	t.Setenv("PATH", dir)
	e, _ := env(t, "linux", &recorder{})
	if _, err := e.Resolve(); !errors.Is(err, rsyncssh.ErrNotInstalled) || !strings.Contains(err.Error(), "rsync not found") {
		t.Errorf("missing rsync: %v", err)
	}
}

func TestResolveHonoursShfRsync(t *testing.T) {
	dir := tools(t, goodRsync)
	other := filepath.Join(t.TempDir(), "rsync")
	os.WriteFile(other, []byte("#!/bin/sh\necho 'rsync  version 3.2.7'\n"), 0o755)
	t.Setenv(rsyncssh.EnvRsync, other)
	e, _ := env(t, "linux", &recorder{})
	e, err := e.Resolve()
	if err != nil || e.Rsync != other || e.SSH != filepath.Join(dir, "ssh") {
		t.Fatalf("Resolve: %+v %v", e, err)
	}
}

func TestServicePath(t *testing.T) {
	for _, tc := range []struct{ goos, in, want string }{
		{"darwin", "", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		{"darwin", "/usr/bin:/opt/homebrew/bin:/Users/a/bin:relative:/bin/",
			"/opt/homebrew/bin:/usr/local/bin:/usr/bin:/Users/a/bin:/bin:/usr/sbin:/sbin"},
		{"linux", "", "/usr/local/bin:/usr/bin:/bin"},
		{"linux", "relative:.", "/usr/local/bin:/usr/bin:/bin"},
		{"linux", "/home/a/.local/bin:/usr/bin:/usr/bin", "/home/a/.local/bin:/usr/bin"},
	} {
		if got := service.ServicePath(tc.goos, tc.in); got != tc.want {
			t.Errorf("ServicePath(%s, %q) = %q, want %q", tc.goos, tc.in, got, tc.want)
		}
	}
}

func TestUnitAndPlistWithoutResolve(t *testing.T) {
	// what Unit and Plist write comes only from Env; nothing empty is set
	e := service.Env{Exe: "/usr/bin/shf"}
	if u := service.Unit(e); strings.Contains(u, "Environment=") {
		t.Errorf("unit:\n%s", u)
	}
	if p := service.Plist(e, "/tmp/shf.log"); strings.Contains(p, "EnvironmentVariables") {
		t.Errorf("plist:\n%s", p)
	}
	e.Path, e.Rsync = "/opt/x%y/bin:/usr/bin", "/opt/x%y/bin/rsync"
	u := service.Unit(e)
	for _, want := range []string{`Environment="PATH=/opt/x%%y/bin:/usr/bin"`, `Environment="SHF_RSYNC=/opt/x%%y/bin/rsync"`} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
}

// Write (shf service install --no-restart, after an automatic update)
// writes the definition and tells the manager, but restarts nothing.
func TestWriteRestartsNothing(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			r := &recorder{}
			tools(t, goodRsync)
			e, _ := env(t, goos, r)
			m, err := service.For(e)
			if err != nil {
				t.Fatal(err)
			}
			if m.Installed() {
				t.Fatal("installed before Write")
			}
			if err := m.Write(); err != nil {
				t.Fatal(err)
			}
			if !m.Installed() {
				t.Fatal("not installed after Write")
			}
			want := ""
			if goos == "linux" {
				want = "systemctl --user daemon-reload"
			}
			if got := strings.Join(r.calls, "\n"); got != want {
				t.Errorf("calls: %q, want %q", got, want)
			}
		})
	}
}
