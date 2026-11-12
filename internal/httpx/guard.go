package httpx

import (
	"fmt"
	"net/netip"
	"syscall"
)

// alwaysBlocked can never be dialed, even with AllowPrivate.
var alwaysBlocked = mustPrefixes(
	"0.0.0.0/8",         // "this network"; 0.0.0.0 reaches localhost on Linux
	"169.254.0.0/16",    // IPv4 link-local, incl. cloud metadata 169.254.169.254
	"224.0.0.0/4",       // IPv4 multicast
	"240.0.0.0/4",       // reserved, incl. broadcast 255.255.255.255
	"::/128",            // unspecified
	"ff00::/8",          // IPv6 multicast
	"fd00:ec2::254/128", // AWS instance metadata over IPv6
)

// privateBlocked can be dialed only with AllowPrivate.
var privateBlocked = mustPrefixes(
	"127.0.0.0/8",    // loopback
	"10.0.0.0/8",     // RFC1918
	"172.16.0.0/12",  // RFC1918
	"192.168.0.0/16", // RFC1918
	"100.64.0.0/10",  // CGNAT (RFC 6598)
	"192.0.0.0/24",   // IETF protocol assignments
	"198.18.0.0/15",  // benchmarking (RFC 2544)
	"::1/128",        // IPv6 loopback
	"fc00::/7",       // unique local addresses
	"fe80::/10",      // IPv6 link-local
)

// IPv6 ranges that embed an IPv4 address; the embedded address is checked.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")   // RFC 6052, v4 in last 32 bits
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48") // RFC 8215, v4 in last 32 bits
	sixToFour      = netip.MustParsePrefix("2002::/16")      // RFC 3056, v4 in bits 16-47
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// checkIP returns an ErrBlockedAddress error if ip may not be dialed.
func checkIP(ip netip.Addr, allowPrivate bool) error {
	if !ip.IsValid() {
		return fmt.Errorf("%w: invalid IP", ErrBlockedAddress)
	}
	orig := ip
	ip = ip.WithZone("").Unmap()

	if ip.Is6() {
		b := ip.As16()
		var v4 netip.Addr
		switch {
		case nat64WellKnown.Contains(ip), nat64Local.Contains(ip):
			v4 = netip.AddrFrom4([4]byte(b[12:16]))
		case sixToFour.Contains(ip):
			v4 = netip.AddrFrom4([4]byte(b[2:6]))
		}
		if v4.IsValid() {
			if err := checkIP(v4, allowPrivate); err != nil {
				return fmt.Errorf("%s embeds %s: %w", orig, v4, err)
			}
		}
	}

	for _, p := range alwaysBlocked {
		if p.Contains(ip) {
			return fmt.Errorf("%w: %s is in %s", ErrBlockedAddress, orig, p)
		}
	}
	if !allowPrivate {
		for _, p := range privateBlocked {
			if p.Contains(ip) {
				return fmt.Errorf("%w: %s is in private range %s (set network.allow_private to permit)", ErrBlockedAddress, orig, p)
			}
		}
	}
	return nil
}

// dialGuard returns a net.Dialer Control hook. It runs after the socket is
// created and before connect(), with the literal IP:port being dialed, so a
// blocked address never sees a single packet.
func dialGuard(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		switch network {
		case "tcp", "tcp4", "tcp6":
		default:
			return fmt.Errorf("%w: network %q not allowed", ErrBlockedAddress, network)
		}
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: cannot parse dial address %q", ErrBlockedAddress, address)
		}
		return checkIP(ap.Addr(), allowPrivate)
	}
}
