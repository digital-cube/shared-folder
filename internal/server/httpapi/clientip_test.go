package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted, err := ParseTrustedProxies("127.0.0.1, 10.1.0.0/16,::1")
	if err != nil {
		t.Fatal(err)
	}
	a := &api{trusted: trusted}
	def, _ := ParseTrustedProxies(DefaultTrustedProxies)
	d := &api{trusted: def}
	for _, c := range []struct {
		a      *api
		remote string
		xff    []string
		want   string
	}{
		// Not from a trusted proxy: X-Forwarded-For is ignored.
		{a, "198.51.100.1:5000", []string{"203.0.113.7"}, "198.51.100.1"},
		{d, "10.1.2.3:5000", []string{"203.0.113.7"}, "10.1.2.3"},
		// From nginx on the same machine, no header: the proxy itself.
		{a, "127.0.0.1:5000", nil, "127.0.0.1"},
		// One proxy.
		{d, "127.0.0.1:5000", []string{"203.0.113.7"}, "203.0.113.7"},
		// Two proxies: client, then the outer proxy (trusted), appended by nginx.
		{a, "127.0.0.1:5000", []string{"203.0.113.7, 10.1.0.1"}, "203.0.113.7"},
		// The outer proxy not trusted: it is the client as far as we know.
		{d, "127.0.0.1:5000", []string{"203.0.113.7, 10.1.0.1"}, "10.1.0.1"},
		// A client that sends its own X-Forwarded-For cannot pick its address:
		// the last untrusted hop wins.
		{a, "127.0.0.1:5000", []string{"1.2.3.4, 203.0.113.7, 10.1.0.1"}, "203.0.113.7"},
		{a, "127.0.0.1:5000", []string{"1.2.3.4", "203.0.113.7", "10.1.0.1"}, "203.0.113.7"},
		// Garbage stops the walk at the last trusted hop.
		{a, "127.0.0.1:5000", []string{"203.0.113.7, nonsense, 10.1.0.1"}, "10.1.0.1"},
		// All trusted: the leftmost.
		{a, "127.0.0.1:5000", []string{"10.1.0.9, 10.1.0.1"}, "10.1.0.9"},
		// IPv6 and v4-mapped.
		{a, "[::1]:5000", []string{"2001:db8::7"}, "2001:db8::7"},
		{d, "[::ffff:127.0.0.1]:5000", []string{"::ffff:203.0.113.7"}, "203.0.113.7"},
	} {
		r := httptest.NewRequest("POST", "/v1/auth/login", nil)
		r.RemoteAddr = c.remote
		for _, v := range c.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := c.a.clientIP(r); got != c.want {
			t.Errorf("%s %q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	for _, bad := range []string{"nonsense", "10.0.0.0/33", "1.2.3"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("ParseTrustedProxies(%q) accepted", bad)
		}
	}
}
