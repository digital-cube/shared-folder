package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/digital-cube/shared-folder/internal/version"
)

func TestRun(t *testing.T) {
	p := Program{Name: "shfx", Summary: "test"}
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{nil, 0, "Usage:", ""},
		{[]string{"help"}, 0, "Usage:", ""},
		{[]string{"--help"}, 0, "Usage:", ""},
		{[]string{"version"}, 0, "shfx " + version.Version + "\n", ""},
		{[]string{"--version"}, 0, "shfx " + version.Version + "\n", ""},
		{[]string{"bogus"}, 2, "", `unknown command "bogus"`},
	}
	for _, tt := range tests {
		var out, errb bytes.Buffer
		code := p.Run(tt.args, &out, &errb)
		if code != tt.code {
			t.Errorf("%v: code %d, want %d", tt.args, code, tt.code)
		}
		if tt.stdout != "" && !strings.Contains(out.String(), tt.stdout) {
			t.Errorf("%v: stdout %q, want it to contain %q", tt.args, out.String(), tt.stdout)
		}
		if tt.stdout == "" && out.Len() != 0 {
			t.Errorf("%v: unexpected stdout %q", tt.args, out.String())
		}
		if tt.stderr != "" && !strings.Contains(errb.String(), tt.stderr) {
			t.Errorf("%v: stderr %q, want it to contain %q", tt.args, errb.String(), tt.stderr)
		}
	}
}
