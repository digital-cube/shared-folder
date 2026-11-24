// Package service installs shf daemon to start with the user's session
// (docs/specifikacija.md, 8.3): a systemd user unit on Linux, a launchd
// agent on macOS. One daemon serves every joined folder.
//
// A service manager starts the daemon with a PATH of its own: launchd's is
// /usr/bin:/bin:/usr/sbin:/sbin, where macOS has Apple's openrsync, which
// shf cannot use. So Install finds rsync and ssh the way the transport
// does (in the installing shell's PATH, or $SHF_RSYNC / $SHF_SSH), refuses
// an rsync that is too old, and writes their absolute paths into the
// definition as $SHF_RSYNC and $SHF_SSH, together with a PATH that puts
// Homebrew ahead of the system directories on macOS. After rsync is
// replaced, "shf service install" has to run again.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/digital-cube/shared-folder/internal/transport/rsyncssh"
)

// Names of what is installed.
const (
	SystemdUnit   = "shf.service"
	LaunchdLabel  = "rs.digitalcube.shf"
	launchdPlist  = LaunchdLabel + ".plist"
	defaultLogDir = "Library/Logs"
)

// Env is what the commands run against; tests replace Run.
type Env struct {
	GOOS   string
	Getenv func(string) string
	// Exe is the absolute path of the shf binary the service runs.
	Exe string
	// StateHome, when not empty, is passed to the daemon as
	// $SHF_STATE_HOME (it was set when the service was installed).
	StateHome string
	// Rsync and SSH are the absolute paths of the tools the daemon runs,
	// passed as $SHF_RSYNC and $SHF_SSH; Resolve finds them.
	Rsync, SSH string
	// Path is the daemon's $PATH; Resolve sets it (ServicePath).
	Path string
	// Run runs a service manager command and returns its combined output.
	Run func(name string, args ...string) ([]byte, error)
}

// macOS: Homebrew on Apple silicon, then Homebrew on Intel (and other
// installs), then the system; launchd's own PATH is only the last four.
var (
	darwinFirst  = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	darwinSystem = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	linuxDefault = []string{"/usr/local/bin", "/usr/bin", "/bin"}
)

// ServicePath is the PATH the daemon runs with. On macOS it is Homebrew's
// directories, then the absolute entries of installPath, then the system
// directories; elsewhere the absolute entries of installPath (the PATH of
// the shell running "shf service install"), or /usr/local/bin:/usr/bin:/bin
// when it has none. Each directory appears once.
func ServicePath(goos, installPath string) string {
	var dirs []string
	seen := map[string]bool{}
	add := func(ds ...string) {
		for _, d := range ds {
			d = filepath.Clean(d)
			// relative entries mean nothing to a service; ':' and newlines
			// would break PATH or the definition
			if !filepath.IsAbs(d) || seen[d] || strings.ContainsAny(d, ":\n") {
				continue
			}
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	own := filepath.SplitList(installPath)
	if goos == "darwin" {
		add(darwinFirst...)
		add(own...)
		add(darwinSystem...)
	} else {
		add(own...)
		if len(dirs) == 0 {
			add(linuxDefault...)
		}
	}
	return strings.Join(dirs, string(filepath.ListSeparator))
}

// Resolve fills in what Install writes and checks it: rsync and ssh
// (rsyncssh.FindTools with env.Rsync and env.SSH: a path given there,
// else $SHF_RSYNC / $SHF_SSH, else the process's PATH; rsync must be new
// enough) and Path (ServicePath of $PATH). Its errors wrap
// rsyncssh.ErrNotInstalled and say what to do; nothing is installed then.
func (env Env) Resolve() (Env, error) {
	getenv := env.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	tools, err := rsyncssh.FindTools(env.Rsync, env.SSH)
	if err != nil {
		return env, err
	}
	env.Rsync, env.SSH = tools.Rsync, tools.SSH
	if env.Path == "" {
		env.Path = ServicePath(env.GOOS, getenv("PATH"))
	}
	return env, nil
}

// Manager is one platform's service manager.
type Manager interface {
	// File is where the unit or agent definition is written.
	File() string
	Install() error
	Uninstall() error
	// Status returns the manager's own report and whether the daemon runs.
	Status() (report string, running bool, err error)
}

// ErrUnsupported: no service manager for this platform (Windows comes later).
var ErrUnsupported = errors.New("shf service supports Linux (systemd) and macOS (launchd)")

// ErrNotInstalled: there is nothing to uninstall or report.
var ErrNotInstalled = errors.New("the shf service is not installed")

// For returns the manager of env.GOOS.
func For(env Env) (Manager, error) {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.Run == nil {
		env.Run = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		}
	}
	home := env.Getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = h
	}
	switch env.GOOS {
	case "linux":
		dir := env.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(home, ".config")
		}
		return &systemd{env: env, file: filepath.Join(dir, "systemd", "user", SystemdUnit)}, nil
	case "darwin":
		return &launchd{env: env, home: home, file: filepath.Join(home, "Library", "LaunchAgents", launchdPlist)}, nil
	}
	return nil, ErrUnsupported
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func run(env Env, name string, args ...string) (string, error) {
	out, err := env.Run(name, args...)
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, s)
		}
		return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return s, nil
}

// ---- systemd ---------------------------------------------------------------

type systemd struct {
	env  Env
	file string
}

var unitTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=shf shared folder client
Documentation=https://github.com/digital-cube/shared-folder
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.Exe}} daemon
{{- range .Environment}}
Environment={{.}}
{{- end}}
Restart=on-failure
RestartSec=10
# SIGTERM: finish the step in progress, then exit
TimeoutStopSec=90

[Install]
WantedBy=default.target
`))

// systemdQuote quotes a word for ExecStart=.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\%$") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

// systemdEnvQuote quotes one NAME=value assignment for Environment=,
// which expands %-specifiers but not $variables.
func systemdEnvQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\%") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

// envVar is one variable the daemon is started with.
type envVar struct{ Name, Value string }

// environment is what the service sets for the daemon, in this order.
func environment(env Env) []envVar {
	var vs []envVar
	for _, v := range []envVar{{"PATH", env.Path}, {rsyncssh.EnvRsync, env.Rsync}, {rsyncssh.EnvSSH, env.SSH}, {"SHF_STATE_HOME", env.StateHome}} {
		if v.Value != "" {
			vs = append(vs, v)
		}
	}
	return vs
}

// Unit is the systemd unit for env.
func Unit(env Env) string {
	var b bytes.Buffer
	data := struct {
		Exe         string
		Environment []string
	}{Exe: systemdQuote(env.Exe)}
	for _, v := range environment(env) {
		data.Environment = append(data.Environment, systemdEnvQuote(v.Name+"="+v.Value))
	}
	unitTmpl.Execute(&b, data)
	return b.String()
}

func (s *systemd) File() string { return s.file }

func (s *systemd) Install() error {
	env, err := s.env.Resolve()
	if err != nil {
		return err
	}
	if err := writeFile(s.file, []byte(Unit(env))); err != nil {
		return err
	}
	if _, err := run(s.env, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	// restart picks up a new binary or unit when it was running already
	if _, err := run(s.env, "systemctl", "--user", "enable", SystemdUnit); err != nil {
		return err
	}
	_, err = run(s.env, "systemctl", "--user", "restart", SystemdUnit)
	return err
}

func (s *systemd) Uninstall() error {
	if _, err := os.Stat(s.file); errors.Is(err, os.ErrNotExist) {
		return ErrNotInstalled
	}
	// a unit that is already stopped is not an error
	run(s.env, "systemctl", "--user", "disable", "--now", SystemdUnit)
	if err := os.Remove(s.file); err != nil {
		return err
	}
	_, err := run(s.env, "systemctl", "--user", "daemon-reload")
	return err
}

func (s *systemd) Status() (string, bool, error) {
	if _, err := os.Stat(s.file); errors.Is(err, os.ErrNotExist) {
		return "", false, ErrNotInstalled
	}
	// is-active exits non-zero for an inactive unit: that is an answer
	state, _ := run(s.env, "systemctl", "--user", "is-active", SystemdUnit)
	report, _ := run(s.env, "systemctl", "--user", "status", "--no-pager", "--lines=10", SystemdUnit)
	return report, state == "active", nil
}

// ---- launchd ---------------------------------------------------------------

type launchd struct {
	env  Env
	home string
	file string
}

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Exe}}</string>
		<string>daemon</string>
	</array>
{{- if .Environment}}
	<key>EnvironmentVariables</key>
	<dict>
{{- range .Environment}}
		<key>{{.Name}}</key>
		<string>{{.Value}}</string>
{{- end}}
	</dict>
{{- end}}
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ExitTimeOut</key>
	<integer>90</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>{{.Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{.Log}}</string>
</dict>
</plist>
`))

// LogFile is where the launchd agent writes the daemon's output.
func (l *launchd) LogFile() string { return filepath.Join(l.home, defaultLogDir, "shf.log") }

// Plist is the launchd agent for env, logging to logFile.
func Plist(env Env, logFile string) string {
	var b bytes.Buffer
	data := struct {
		Label, Exe, Log string
		Environment     []envVar
	}{Label: LaunchdLabel, Exe: xmlEscape(env.Exe), Log: xmlEscape(logFile)}
	for _, v := range environment(env) {
		data.Environment = append(data.Environment, envVar{xmlEscape(v.Name), xmlEscape(v.Value)})
	}
	plistTmpl.Execute(&b, data)
	return b.String()
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

func (l *launchd) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (l *launchd) File() string { return l.file }

func (l *launchd) Install() error {
	env, err := l.env.Resolve()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.LogFile()), 0o755); err != nil {
		return err
	}
	if err := writeFile(l.file, []byte(Plist(env, l.LogFile()))); err != nil {
		return err
	}
	// bootout first, so a reinstall loads the new definition
	run(l.env, "launchctl", "bootout", l.domain()+"/"+LaunchdLabel)
	_, err = run(l.env, "launchctl", "bootstrap", l.domain(), l.file)
	return err
}

func (l *launchd) Uninstall() error {
	if _, err := os.Stat(l.file); errors.Is(err, os.ErrNotExist) {
		return ErrNotInstalled
	}
	run(l.env, "launchctl", "bootout", l.domain()+"/"+LaunchdLabel)
	return os.Remove(l.file)
}

func (l *launchd) Status() (string, bool, error) {
	if _, err := os.Stat(l.file); errors.Is(err, os.ErrNotExist) {
		return "", false, ErrNotInstalled
	}
	out, err := run(l.env, "launchctl", "print", l.domain()+"/"+LaunchdLabel)
	if err != nil {
		return "not loaded (launchctl print failed)\nlog: " + l.LogFile(), false, nil
	}
	running := strings.Contains(out, "state = running")
	return out + "\nlog: " + l.LogFile(), running, nil
}
