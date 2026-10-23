package core

import (
	"net"
	"net/netip"
)

var (
	globalUnicast = netip.MustParsePrefix("2000::/3")
	teredo        = netip.MustParsePrefix("2001::/32")
)

// NativeIPv6 reports whether this PC reaches the IPv6 internet on its own.
// Without it the TUN must stay IPv4-only: an IPv6 default route through the
// TUN makes browsers use AAAA records the direct outbound can never reach, so
// dual-stack sites (Google, Microsoft) reset right after connecting.
//
// A UDP "connect" sends no packet; it only asks the routing table for a path
// and a source address. Only a global, non-Teredo source counts: a ULA from a
// router or an overlay VPN has no route to the internet.
func NativeIPv6() bool {
	conn, err := net.Dial("udp6", "[2001:4860:4860::8888]:53")
	if err != nil {
		return false
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return false
	}
	addr, ok := netip.AddrFromSlice(local.IP)
	return ok && addr.Is6() && globalUnicast.Contains(addr) && !teredo.Contains(addr)
}
