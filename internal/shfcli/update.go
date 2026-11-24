package shfcli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
	"github.com/digital-cube/shared-folder/internal/service"
	"github.com/digital-cube/shared-folder/internal/update"
	"github.com/digital-cube/shared-folder/internal/version"
)

// infoTimeout bounds the server checks of shf version and shf status.
const infoTimeout = 5 * time.Second

func (env Env) checker() *update.Checker {
	goos, goarch := env.GOOS, env.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return &update.Checker{HTTP: env.Options.HTTPClient, Current: version.Version, GOOS: goos, GOARCH: goarch}
}

// exe is the absolute path of the running shf, symlinks resolved.
func (env Env) exe() (string, error) {
	if env.Exe != "" {
		return env.Exe, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

// joinedServers are the server URLs of the joined folders.
func joinedServers(env Env) ([]string, error) {
	home, err := client.StateHome(env.Getenv)
	if err != nil {
		return nil, err
	}
	cfgs, err := client.Joined(home)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, c := range cfgs {
		urls = append(urls, c.URL)
	}
	return update.Servers(urls), nil
}

func updateCmd(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only say which version is available")
	server := fs.String("server", "", "the server to follow (default: the server of the joined folders)")
	yes := fs.Bool("yes", false, "do not ask before installing")
	if _, err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	ck := env.checker()
	var c *update.Check
	var err error
	if *server != "" {
		c, err = ck.Check(ctx, *server)
	} else {
		var servers []string
		if servers, err = joinedServers(env); err != nil {
			return err
		}
		c, err = ck.Resolve(ctx, servers)
	}
	if err != nil {
		return err
	}
	w := env.Stdout
	fmt.Fprintln(w, c.String())
	if *check || !c.Available() {
		return nil
	}
	exe, err := env.exe()
	if err != nil {
		return err
	}
	if !*yes && !confirm(env, fmt.Sprintf("replace %s with shf %s? [y/N] ", exe, c.ServerVersion)) {
		fmt.Fprintln(w, "not updated")
		return nil
	}
	in := &update.Installer{HTTP: env.Options.HTTPClient, Exe: exe}
	st, err := in.Prepare(ctx, c, "")
	if nw := (*update.NotWritableError)(nil); errors.As(err, &nw) {
		return notWritable(ctx, in, c, nw, env)
	}
	if err != nil {
		return err
	}
	if err := in.Install(st); err != nil {
		return err
	}
	fmt.Fprintf(w, "installed shf %s at %s (was %s)\n", st.Version, exe, c.Current)
	return restartService(ctx, exe, env)
}

// confirm asks on env.Stdin; anything but y or yes (or no answer) is no.
func confirm(env Env, question string) bool {
	if env.Stdin == nil {
		fmt.Fprintln(env.Stdout, "no terminal to ask on; run with --yes to install")
		return false
	}
	fmt.Fprint(env.Stdout, question)
	line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// notWritable stages the new binary where the user may write and says
// how to put it in place.
func notWritable(ctx context.Context, in *update.Installer, c *update.Check, nw *update.NotWritableError, env Env) error {
	dir, err := os.MkdirTemp("", "shf-update-")
	if err != nil {
		return nw
	}
	st, err := in.Prepare(ctx, c, dir)
	if err != nil {
		os.RemoveAll(dir)
		return fmt.Errorf("%w; downloading the update elsewhere failed too: %v", nw, err)
	}
	staged := filepath.Join(dir, "shf")
	if err := os.Rename(st.Path, staged); err != nil {
		staged = st.Path
	}
	return fmt.Errorf("%w\nshf %s is downloaded and verified in %s; install it as the owner of %s:\n\n  sudo install -m 0755 %s %s\n\nand then, when the service is installed:\n\n  shf service install",
		nw, st.Version, dir, nw.Dir, shellQuote(staged), shellQuote(in.Exe))
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`&;|<>()*?[]{}#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// restartService runs "<new shf> service install" when the service is
// installed: the new binary writes its own unit or plist, with the rsync
// and ssh found as by shf service install, and restarts the daemon.
func restartService(ctx context.Context, exe string, env Env) error {
	m, err := service.For(service.Env{GOOS: runtime.GOOS, Getenv: env.Getenv, Exe: exe})
	if err != nil || !m.Installed() {
		fmt.Fprintln(env.Stdout, "the service is not installed; a running shf daemon keeps the old version until it is started again")
		return nil
	}
	fmt.Fprintf(env.Stdout, "restarting the service (%s service install)\n", exe)
	cmd := exec.CommandContext(ctx, exe, "service", "install")
	cmd.Stdout, cmd.Stderr = env.Stdout, env.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shf was updated, but the service was not restarted: %w; run shf service install", err)
	}
	return nil
}

// rewriteService is the daemon's AfterInstall: "<new shf> service install
// --no-restart" when the service is installed, so that the next start
// runs with the new binary's definition; the daemon restarts itself.
func rewriteService(exe string, env Env, logf func(string)) func(context.Context) error {
	return func(ctx context.Context) error {
		m, err := service.For(service.Env{GOOS: runtime.GOOS, Getenv: env.Getenv, Exe: exe})
		if err != nil || !m.Installed() {
			return nil
		}
		out, err := exec.CommandContext(ctx, exe, "service", "install", "--no-restart").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s service install --no-restart: %w: %s", exe, err, strings.TrimSpace(string(out)))
		}
		logf("service definition written anew: " + m.File())
		return nil
	}
}

// versionCmd is shf version: this shf's version, then each server's and
// whether there is an update; --short only the first line.
func versionCmd(ctx context.Context, args []string, env Env) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	short := fs.Bool("short", false, "only this shf's version")
	if _, err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "shf %s\n", version.Version)
	if *short {
		return nil
	}
	servers, err := joinedServers(env)
	if err != nil || len(servers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	lines := make([]string, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lines[i] = "server " + s + ": " + serverLine(ctx, env.checker(), s)
		}()
	}
	wg.Wait()
	for _, l := range lines {
		fmt.Fprintln(env.Stdout, l)
	}
	return nil
}

// serverLine says which version server runs and what that means for this
// shf.
func serverLine(ctx context.Context, ck *update.Checker, server string) string {
	c, err := ck.Check(ctx, server)
	if err != nil {
		return "version unknown (" + err.Error() + ")"
	}
	return c.ServerVersion + ", " + verdict(c)
}

func verdict(c *update.Check) string {
	switch {
	case c.UpToDate():
		return "up to date"
	case c.Available():
		return "update available (shf update)"
	case !c.Downloads:
		return "another version than this shf; the server publishes no packages"
	}
	return fmt.Sprintf("another version than this shf; the server has no package of it for %s/%s", c.GOOS, c.GOARCH)
}

// statusVersion is the version line of shf status.
func statusVersion(ctx context.Context, env Env, server string) string {
	ctx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	return fmt.Sprintf("shf %s; server %s", version.Version, serverLine(ctx, env.checker(), server))
}

// daemonUpdater is the automatic update of shf daemon, or nil when
// settings.json turns it off.
func daemonUpdater(env Env, home string, set client.Settings, exe string, logf func(string)) *update.Auto {
	if !set.AutoUpdate {
		return nil
	}
	return &update.Auto{
		Checker:   env.checker(),
		Installer: &update.Installer{HTTP: env.Options.HTTPClient, Exe: exe},
		Servers: func() ([]string, error) {
			cfgs, err := client.Joined(home)
			var urls []string
			for _, c := range cfgs {
				urls = append(urls, c.URL)
			}
			return urls, err
		},
		AfterInstall: rewriteService(exe, env, logf),
	}
}
