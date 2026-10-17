package httpfetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// lookupFunc resolves a hostname to its addresses. It is a parameter rather than
// a package-level hook so address-policy tests can supply a fixed answer set
// without touching process DNS.
type lookupFunc func(ctx context.Context, hostname string) ([]netip.Addr, error)

// systemLookup resolves with the process resolver.
func systemLookup(ctx context.Context, hostname string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", hostname)
}

// resolveAddresses returns the checked address set for one hop.
func (p *Provider) resolveAddresses(ctx context.Context, hostname string) ([]netip.Addr, error) {
	if p.resolve != nil {
		return p.resolve(ctx, hostname)
	}
	return resolvePublicAddresses(ctx, hostname, systemLookup)
}

// resolvePublicAddresses resolves one hostname and refuses the whole answer set
// when any address is not public.
//
// A hostname with several answers is only safe to fetch when every answer can
// be dialed: keeping the set and dialing the first public entry would leave the
// choice of destination to the resolver's ordering, which a hostile or
// rebinding DNS answer controls. An IP literal skips resolution because there is
// nothing to resolve; it is still checked.
func resolvePublicAddresses(ctx context.Context, hostname string, lookup lookupFunc) ([]netip.Addr, error) {
	host := stripIPv6Brackets(hostname)
	var addrs []netip.Addr
	if literal, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{literal}
	} else {
		resolved, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		addrs = resolved
	}
	if len(addrs) == 0 {
		return nil, newError(codeProviderError, "hostname %q resolved to no addresses", hostname)
	}
	for _, addr := range addrs {
		if !addr.IsValid() {
			return nil, newError(codeProviderError, "hostname %q resolved to an invalid IP address", hostname)
		}
		if !isPublicAddress(addr) {
			return nil, newError(codeBlockedURL, "URL hostname %q resolves to a non-public IP address", hostname)
		}
	}
	return addrs, nil
}

// isPublicAddress reports whether addr is globally reachable unicast.
//
// An IPv4-mapped IPv6 address is judged by the IPv4 address it carries, which is
// how the upstream ipaddr.js range check behaves. A zone index is ignored for
// the decision: `fe80::1%eth0` must classify as link-local, and netip prefix
// matching would otherwise reject the zone and call it public.
func isPublicAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	addr = addr.WithZone("")
	if addr.Is4() {
		return !matchesAny(addr, nonPublicIPv4)
	}
	return !matchesAny(addr, nonPublicIPv6)
}

// matchesAny reports whether addr falls inside any prefix in prefixes.
func matchesAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// nonPublicIPv4 lists every IPv4 range that is not globally reachable unicast:
// unspecified, private, carrier-grade NAT, loopback, link-local, reserved,
// documentation, benchmarking, AS112, AMT, multicast, and the reserved block
// that contains broadcast. The set is the upstream ipaddr.js range table, so a
// port does not disagree with the original about a destination.
var nonPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // unspecified
	netip.MustParsePrefix("10.0.0.0/8"),      // private
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("127.0.0.0/8"),     // loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local
	netip.MustParsePrefix("172.16.0.0/12"),   // private
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.31.196.0/24"), // AS112
	netip.MustParsePrefix("192.52.193.0/24"), // AMT
	netip.MustParsePrefix("192.88.99.0/24"),  // reserved
	netip.MustParsePrefix("192.168.0.0/16"),  // private
	netip.MustParsePrefix("192.175.48.0/24"), // AS112
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, including broadcast
}

// nonPublicIPv6 lists the IPv6 ranges that are not globally reachable unicast.
// It follows the upstream ipaddr.js range table: unspecified, loopback,
// IPv4-translated, NAT64, discard, IETF protocol assignments (which cover
// Teredo, benchmarking, AMT, AS112, ORCHID, and drone remote ID), documentation,
// 6to4, AS112, segment routing, unique-local, link-local, deprecated site-local,
// and multicast. The well-known NAT64 prefix translates to arbitrary IPv4, so it
// is refused before the translation rather than dialed.
var nonPublicIPv6 = []netip.Prefix{
	netip.MustParsePrefix("::/128"),            // unspecified
	netip.MustParsePrefix("::1/128"),           // loopback
	netip.MustParsePrefix("::ffff:0:0:0/96"),   // IPv4-translated (RFC 6145)
	netip.MustParsePrefix("64:ff9b::/96"),      // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"),    // NAT64 local-use prefix
	netip.MustParsePrefix("100::/64"),          // discard
	netip.MustParsePrefix("2001::/23"),         // IETF protocol assignments
	netip.MustParsePrefix("2001:db8::/32"),     // documentation
	netip.MustParsePrefix("2002::/16"),         // 6to4
	netip.MustParsePrefix("2620:4f:8000::/48"), // AS112
	netip.MustParsePrefix("3fff::/20"),         // documentation
	netip.MustParsePrefix("5f00::/16"),         // segment routing
	netip.MustParsePrefix("fc00::/7"),          // unique-local
	netip.MustParsePrefix("fe80::/10"),         // link-local
	netip.MustParsePrefix("fec0::/10"),         // deprecated site-local
	netip.MustParsePrefix("ff00::/8"),          // multicast
}

// pinnedDial builds the connector for one hop: whatever host the transport asks
// for, the connection goes to one of the addresses that passed the policy, with
// the request's port. The URL hostname stays on the request, so the Host header
// and the TLS server name still name the origin.
func pinnedDial(addresses []netip.Addr) func(ctx context.Context, network, addr string) (net.Conn, error) {
	// The resolver contract guarantees a non-empty set. A replacement that
	// breaks it must fail instead of falling back to the unvalidated host.
	if len(addresses) == 0 {
		return func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("httpfetch: no validated address to dial")
		}
	}
	dialer := &net.Dialer{KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("httpfetch: split dial address %q: %w", addr, err)
		}
		failures := make([]error, 0, len(addresses))
		for _, address := range addresses {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			if err == nil {
				return conn, nil
			}
			failures = append(failures, err)
		}
		return nil, fmt.Errorf("httpfetch: no validated address for %s reachable: %w", addr, errors.Join(failures...))
	}
}

// stripIPv6Brackets removes the brackets a URL keeps around an IPv6 hostname.
// netip and the dialer take the bare address.
func stripIPv6Brackets(hostname string) string {
	if strings.HasPrefix(hostname, "[") && strings.HasSuffix(hostname, "]") {
		return hostname[1 : len(hostname)-1]
	}
	return hostname
}
