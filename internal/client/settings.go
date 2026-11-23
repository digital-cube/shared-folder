package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SettingsFile is the name of the client's settings in the state home.
const SettingsFile = "settings.json"

// Duration is a time.Duration written as in Go ("2s", "1m30s", "24h").
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("want a duration such as \"2s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Settings are the intervals of the background client and the limits of
// the mass-delete guard (docs/specifikacija.md, 8.3 and 8.4). They hold
// for every joined folder. Keys missing from settings.json keep their
// defaults.
type Settings struct {
	// Debounce: local changes are collected this long after the last one
	// before a cycle starts.
	Debounce Duration `json:"debounce"`
	// Settle: a file is sent only once its size and mtime have not changed
	// for this long.
	Settle Duration `json:"settle"`
	// Poll: how often the server is asked for changes while the WebSocket
	// is down.
	Poll Duration `json:"poll"`
	// FullSync: how often the whole server state is compared (also at
	// every start).
	FullSync Duration `json:"full_sync"`
	// ReconnectMax caps the growing pause between WebSocket reconnects.
	ReconnectMax Duration `json:"reconnect_max"`
	// Ping: how often the client pings the server over the WebSocket; no
	// pong within the same time means the connection is dead.
	Ping Duration `json:"ping"`
	// RetryMax caps the pause before a failed cycle is retried.
	RetryMax Duration `json:"retry_max"`
	// MassDelete are the limits of the mass-delete guard.
	MassDelete MassDelete `json:"mass_delete"`
}

// DefaultSettings are the values of the specification.
func DefaultSettings() Settings {
	return Settings{
		Debounce:     Duration(2 * time.Second),
		Settle:       Duration(3 * time.Second),
		Poll:         Duration(60 * time.Second),
		FullSync:     Duration(24 * time.Hour),
		ReconnectMax: Duration(60 * time.Second),
		Ping:         Duration(30 * time.Second),
		RetryMax:     Duration(60 * time.Second),
		MassDelete:   DefaultMassDelete,
	}
}

// LoadSettings reads <home>/settings.json over the defaults; a missing
// file means the defaults.
func LoadSettings(home string) (Settings, error) {
	s := DefaultSettings()
	file := filepath.Join(home, SettingsFile)
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", file, err)
	}
	for name, d := range map[string]Duration{"debounce": s.Debounce, "poll": s.Poll, "full_sync": s.FullSync,
		"reconnect_max": s.ReconnectMax, "ping": s.Ping, "retry_max": s.RetryMax} {
		if d <= 0 {
			return s, fmt.Errorf("%s: %s must be positive", file, name)
		}
	}
	if s.Settle < 0 {
		return s, fmt.Errorf("%s: settle must not be negative", file)
	}
	return s, nil
}
