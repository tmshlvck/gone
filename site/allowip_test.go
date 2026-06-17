package site

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// AllowedIPs lets through addresses inside an entry and 403s the rest, across
// CIDR entries, bare-IP entries, IPv4 and IPv6, and the two RemoteAddr shapes
// (ip:port from net/http, bare ip as middleware.RealIP can leave it).
func TestAllowedIPs(t *testing.T) {
	mw := AllowedIPs(
		"203.0.113.0/24", // CIDR, IPv4
		"198.51.100.7",   // bare host, IPv4
		"2001:db8::/32",  // CIDR, IPv6
		"::1",            // bare host, IPv6
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // sentinel: "request reached the handler"
	})
	handler := mw(next)

	cases := []struct {
		name       string
		remoteAddr string
		want       int
	}{
		{"v4 in cidr", "203.0.113.9:5000", http.StatusTeapot},
		{"v4 cidr edge", "203.0.113.255:1", http.StatusTeapot},
		{"v4 outside cidr", "203.0.114.1:5000", http.StatusForbidden},
		{"v4 bare host match", "198.51.100.7:42", http.StatusTeapot},
		{"v4 bare host miss", "198.51.100.8:42", http.StatusForbidden},
		{"v6 in cidr", "[2001:db8::1]:5000", http.StatusTeapot},
		{"v6 outside cidr", "[2001:dead::1]:5000", http.StatusForbidden},
		{"v6 loopback bare entry", "[::1]:5000", http.StatusTeapot},
		{"bare ip no port (RealIP)", "203.0.113.9", http.StatusTeapot},
		{"v4-mapped-v6 against v4 prefix", "[::ffff:203.0.113.9]:7", http.StatusTeapot},
		{"unparseable remoteaddr denied", "garbage", http.StatusForbidden},
		{"empty remoteaddr denied", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("RemoteAddr %q: status %d, want %d", tc.remoteAddr, rec.Code, tc.want)
			}
		})
	}
}

// An empty allow-list denies everything (no entry can contain any address).
func TestAllowedIPs_EmptyDeniesAll(t *testing.T) {
	handler := AllowedIPs()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:5000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// A malformed entry is a startup misconfiguration: AllowedIPs panics when built.
func TestAllowedIPs_PanicsOnBadEntry(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on invalid entry, got none")
		}
	}()
	AllowedIPs("not-an-ip")
}
