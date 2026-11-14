package httpx

import (
	"fmt"
	"net/netip"
	"syscall"
)

var alwaysBlocked = mustPrefixes(
	"0.0.0.0/8",
	"169.254.0.0/16",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"ff00::/8",
	"fd00:ec2::254/128",
)

var privateBlocked = mustPrefixes(
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"198.18.0.0/15",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
)

var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local     = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour      = netip.MustParsePrefix("2002::/16")
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
