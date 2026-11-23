package ignore

import (
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitCompat builds random trees with random nested ignore files and
// compares Match with what git itself reports as ignored. The built-in and
// client-temp lists are switched off, since git knows nothing about them.
func TestGitCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("slow; skipped with -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dirs := []string{"", "a", "b", "a/b", "a/c", "a/b/c", "b/a", "build", "logs", "logs/old", "x", "x/y", "x/y/z"}
	names := []string{"a.log", "keep.log", "x.txt", "build", "foo", "readme.md", "z", "c", "b", "Main.go"}
	pool := []string{
		"*.log", "!keep.log", "!*.log", "!**/keep.log", "!a.log", "*.txt", "!readme.md", "!*.go",
		"build/", "build", "/foo", "foo/", "a/", "!a/", "b/*", "!b/a", "*/b", "**/b/**",
		"**/z", "z/", "!z", "x/**", "!x/y/", "!x/y/z", "x/y", "/x/", "*", "!*/",
		"a/**/c", "/a/b", "c/", "!c", "**/old", "logs/**", "!logs/old/", "?", "[ab]",
	}
	r := rand.New(rand.NewPCG(7, 9))
	for trial := range 150 {
		root := t.TempDir()
		var paths []string
		for _, d := range dirs {
			for _, n := range names {
				p := strings.TrimPrefix(d+"/"+n, "/")
				if r.IntN(3) == 0 || isDirInSet(dirs, p) {
					continue
				}
				paths = append(paths, p)
			}
		}
		rules := map[string][]byte{}
		for _, d := range dirs {
			if r.IntN(3) != 0 {
				continue
			}
			var lines []string
			for n := r.IntN(4) + 1; n > 0; n-- {
				lines = append(lines, pool[r.IntN(len(pool))])
			}
			rules[d] = []byte(strings.Join(lines, "\n") + "\n")
			paths = append(paths, strings.TrimPrefix(d+"/.gitignore", "/"))
		}
		for _, p := range paths {
			mustWrite(t, filepath.Join(root, p), nil)
		}
		for d, c := range rules {
			mustWrite(t, filepath.Join(root, d, ".gitignore"), c)
		}

		ignored := gitIgnored(t, root)
		m := New(rules)
		m.defaults, m.forced = nil, nil
		for _, p := range paths {
			if got := m.Match(p, false); got != ignored[p] {
				t.Fatalf("trial %d: Match(%q) = %v, git says %v; rules %q", trial, p, got, ignored[p], rules)
			}
		}
	}
}

func isDirInSet(dirs []string, p string) bool {
	for _, d := range dirs {
		if d == p || strings.HasPrefix(d, p+"/") {
			return true
		}
	}
	return false
}

func mustWrite(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitIgnored returns the set of files under root that git considers ignored.
func gitIgnored(t *testing.T, root string) map[string]bool {
	t.Helper()
	cmd := exec.Command("sh", "-c", "git init -q . && git ls-files --others --ignored --exclude-standard")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "XDG_CONFIG_HOME="+root+"/.noconfig")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git: %v", err)
	}
	set := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" {
			set[l] = true
		}
	}
	return set
}
