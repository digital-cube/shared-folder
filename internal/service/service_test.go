package service_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/service"
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

func env(t *testing.T, goos string, r *recorder) (service.Env, string) {
	home := t.TempDir()
	return service.Env{
		GOOS:      goos,
		Getenv:    func(k string) string { return map[string]string{"HOME": home}[k] },
		Exe:       "/opt/my apps/shf",
		StateHome: "/data/shf state",
		Run:       r.run,
	}, home
}

func TestSystemd(t *testing.T) {
	r := &recorder{reply: map[string]string{"systemctl --user is-active": "active", "systemctl --user status": "● shf.service - shf shared folder client"}}
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
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist lacks %q:\n%s", want, plist)
		}
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
