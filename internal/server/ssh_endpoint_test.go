package server

import "testing"

func TestParseSSHEndpoint(t *testing.T) {
	ok := map[string]SSHEndpoint{
		"":                      {},
		"files.example.org":     {Host: "files.example.org"},
		"shfd@files:2222":       {User: "shfd", Host: "files", Port: 2222},
		"shfd@[2001:db8::1]:22": {User: "shfd", Host: "2001:db8::1", Port: 22},
		"[2001:db8::1]":         {Host: "2001:db8::1"},
	}
	for in, want := range ok {
		got, err := ParseSSHEndpoint(in)
		if err != nil || got != want {
			t.Errorf("ParseSSHEndpoint(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"shfd@", "h:0", "h:99999", "h:x", "[::1", "a b@h"} {
		if _, err := ParseSSHEndpoint(in); err == nil {
			t.Errorf("ParseSSHEndpoint(%q) accepted", in)
		}
	}
}
