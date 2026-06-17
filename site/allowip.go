package site

import (
	"net"
	"net/http"
	"net/netip"
)

// AllowedIPs returns middleware that rejects any request whose source address
// is not inside one of the given allow-list entries, answering 403 Forbidden.
// Each entry is either a CIDR prefix ("203.0.113.0/24", "2001:db8::/32") or a
// bare address ("127.0.0.1", "::1"), which is treated as a single-host /32 or
// /128. IPv4 and IPv6 are both supported. It is the source-IP gate for an
// internal dashboard that should answer only a known set of operator networks.
//
// It matches against r.RemoteAddr, so WHERE you place it in the chain decides
// what "source" means — and that depends on your deployment:
//
//   - Standalone (the app is the thing clients connect to): RemoteAddr is the
//     real TCP peer. Use AllowedIPs alone, WITHOUT middleware.RealIP. Adding
//     RealIP here would let a client forge X-Forwarded-For and walk straight
//     through the allow-list.
//   - Behind a trusted reverse proxy (Caddy / nginx / Apache / HAProxy):
//     RemoteAddr is the proxy until middleware.RealIP rewrites it from the
//     proxy's forwarded header. Put RealIP FIRST, then AllowedIPs, so the
//     list matches the real client. See docs/BEHIND_PROXY.md.
//
// Matching is a linear O(N) scan over the entries — fine for the handful of
// operator prefixes this is meant for; it is not built for large block-lists.
// Entries are parsed once when the middleware is built; an invalid entry is a
// configuration error and panics at construction (startup), not per request.
//
//	mux.Use(site.AllowedIPs("203.0.113.0/24", "2001:db8::/32", "127.0.0.1"))
func AllowedIPs(entries ...string) func(http.Handler) http.Handler {
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		prefixes = append(prefixes, parseAllowEntry(e))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if addr, ok := remoteAddr(r); ok && allowed(addr, prefixes) {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "Forbidden", http.StatusForbidden)
		})
	}
}

// parseAllowEntry accepts a CIDR ("10.0.0.0/8") or a bare address
// ("10.0.0.1", treated as a single host). It panics on anything else — a
// malformed allow-list is a startup misconfiguration, not a runtime condition.
func parseAllowEntry(e string) netip.Prefix {
	if p, err := netip.ParsePrefix(e); err == nil {
		return p.Masked()
	}
	if a, err := netip.ParseAddr(e); err == nil {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()) // /32 or /128
	}
	panic("site.AllowedIPs: invalid entry " + e + " (want a CIDR or an IP address)")
}

// remoteAddr extracts the comparable IP from r.RemoteAddr. It tolerates both
// the "ip:port" net/http produces and a bare "ip" (which middleware.RealIP can
// leave behind when it rewrites RemoteAddr from a forwarded header). The
// address is unmapped so an IPv4-in-IPv6 form (::ffff:203.0.113.5) compares
// against plain IPv4 prefixes.
func remoteAddr(r *http.Request) (netip.Addr, bool) {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func allowed(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
