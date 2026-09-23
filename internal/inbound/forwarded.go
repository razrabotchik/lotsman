package inbound

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// forwardingHeaders are the headers that claim a request came from somewhere
// other than where it came from. `X-Forwarded-*` is matched by prefix because
// the family is open-ended and a header nobody anticipated is exactly the one
// a later change would read without thinking.
var forwardingHeaders = []string{"Forwarded", "X-Real-Ip"}

const forwardedPrefix = "X-Forwarded-"

// StripUntrustedForwarding removes forwarding headers from a request that did
// not arrive from a configured proxy (FR-85).
//
// Removing rather than ignoring is the point. "Ignore them" is a rule every
// future reader of the request has to know and obey; deleting them means the
// claim is not in the request at all, and a later feature that reads
// `X-Forwarded-For` to decide something gets the truth without having been
// told to be careful.
func StripUntrustedForwarding(next http.Handler, trustedProxies []string) (http.Handler, error) {
	trusted, err := parseTrusted(trustedProxies)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromTrustedProxy(r.RemoteAddr, trusted) {
			for _, name := range forwardingHeaders {
				r.Header.Del(name)
			}
			for name := range r.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), forwardedPrefix) {
					r.Header.Del(name)
				}
			}
		}
		next.ServeHTTP(w, r)
	}), nil
}

// parseTrusted turns the configured addresses and CIDRs into prefixes. A bare
// address becomes a single-address prefix, so there is one comparison rather
// than two code paths.
func parseTrusted(trustedProxies []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(trustedProxies))
	for _, entry := range trustedProxies {
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, errs.Errorf(errs.ClassUsage, "inbound: trusted proxy %q is not a CIDR", entry)
			}
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "inbound: trusted proxy %q is not an IP address", entry)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// fromTrustedProxy reports whether the peer is one the operator named.
//
// An address that cannot be parsed is not trusted. That is the only safe
// reading: the alternative is deciding to believe a header because the peer
// was unidentifiable.
func fromTrustedProxy(remoteAddr string, trusted []netip.Prefix) bool {
	if len(trusted) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
