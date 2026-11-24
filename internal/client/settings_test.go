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
	if d := client.DefaultSettings(); time.Duration(d.TransportRetry) != 5*time.Second || time.Duration(d.TransportRetryMax) != 5*time.Minute {
		t.Errorf("transport retry defaults: %v, %v", d.TransportRetry, d.TransportRetryMax)
	}
	if d := client.DefaultSettings(); !d.AutoUpdate || time.Duration(d.UpdateInterval) != 24*time.Hour {
		t.Errorf("update defaults: %v, %v", d.AutoUpdate, d.UpdateInterval)
	}
	write(`{"auto_update": false, "update_interval": "6h"}`)
	if s, err = client.LoadSettings(home); err != nil || s.AutoUpdate || time.Duration(s.UpdateInterval) != 6*time.Hour {
		t.Errorf("auto_update false: %+v %v", s, err)
	}
	for _, bad := range []string{`{"update_interval": "0s"}`, `{"bwlimit": -1}`, `{"poll": "0s"}`, `{"settle": "-1s"}`, `{`,
		`{"transport_retry": "0s"}`, `{"transport_retry": "10m", "transport_retry_max": "5m"}`} {
		write(bad)
		if _, err := client.LoadSettings(home); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}
