package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
)

// Server is one relay candidate from a public feed.
type Server struct {
	Host, IP string
	Source   string
	Identity string
	Proxy    *Proxy
}

// Directory is the merged result of one or more feeds.
type Directory struct {
	Servers              []Server
	Rejected, Duplicates int
	Sources              []SourceResult
}

// SourceResult reports one feed's outcome for the log.
type SourceResult struct {
	Name       string
	Candidates int
	Err        error
}

// Protocol returns the relay protocol, or "" for a server without relay settings.
func (s Server) Protocol() string {
	if s.Proxy != nil {
		return s.Proxy.Type
	}
	return ""
}

// IndirectTransport reports a WebSocket front door. It may terminate at a
// nearby CDN edge, so its TCP RTT is not a useful estimate of the proxy exit
// path; only authenticated checks are.
func (s Server) IndirectTransport() bool {
	return s.Proxy != nil && s.Proxy.Transport != nil
}

// Key identifies a relay by its connection settings, so different credentials
// on one endpoint remain distinct candidates.
func (s Server) Key() string {
	if s.Identity != "" {
		return s.Identity
	}
	if s.Proxy == nil {
		return s.IP
	}
	b, _ := json.Marshal(s.Proxy)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// PinnedTo returns s connecting to ip. The identity and the TLS server name
// stay those of the original hostname, so DNS pinning changes neither
// session-local quality history nor certificate validation.
func (s Server) PinnedTo(ip string) Server {
	s.Identity = s.Key()
	if s.Proxy != nil {
		p := *s.Proxy
		if p.TLS != nil {
			t := *p.TLS
			if t.ServerName == "" {
				t.ServerName = p.Server
			}
			p.TLS = &t
		}
		p.Server = ip
		s.Proxy = &p
	}
	s.IP = ip
	return s
}

var reservedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"),
}

// PublicIPv4 reports whether text is a globally routable IPv4 address: not
// private, shared (CGNAT), loopback, link-local, benchmark, documentation,
// multicast, or reserved.
func PublicIPv4(text string) bool {
	ip, err := netip.ParseAddr(text)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range reservedIPv4 {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// LookupPublicIPv4 resolves host and returns its first public IPv4 address,
// or "" without an error when it has only non-public addresses.
func LookupPublicIPv4(ctx context.Context, host string) (string, error) {
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	for _, ip := range addrs {
		if PublicIPv4(ip.String()) {
			return ip.String(), nil
		}
	}
	return "", nil
}
