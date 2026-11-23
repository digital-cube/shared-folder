package client_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/digital-cube/shared-folder/internal/client"
)

func TestLoadSettings(t *testing.T) {
	home := t.TempDir()
	s, err := client.LoadSettings(home)
	if err != nil || s != client.DefaultSettings() {
		t.Fatalf("no file: %+v %v", s, err)
	}
	write := func(js string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, client.SettingsFile), []byte(js), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"debounce": "200ms", "bwlimit": 2048, "mass_delete": {"files": 5}}`)
	s, err = client.LoadSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(s.Debounce) != 200*time.Millisecond || s.BwLimit != 2048 || s.MassDelete.Files != 5 ||
		s.MassDelete.Percent != client.DefaultMassDelete.Percent || time.Duration(s.Poll) != time.Minute {
		t.Errorf("settings: %+v", s)
	}
	for _, bad := range []string{`{"bwlimit": -1}`, `{"poll": "0s"}`, `{"settle": "-1s"}`, `{`} {
		write(bad)
		if _, err := client.LoadSettings(home); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}
