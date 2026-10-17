package httpfetch

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestIsPublicAddress(t *testing.T) {
	public := []string{
		"8.8.8.8",
		"1.1.1.1",
		"2001:4860:4860::8888",
		"::ffff:8.8.8.8",
	}
	for _, raw := range public {
		addr := netip.MustParseAddr(raw)
		if !isPublicAddress(addr) {
			t.Errorf("isPublicAddress(%s) = false, want true", raw)
		}
	}

	nonPublic := []string{
		// IPv4: unspecified, private, CGNAT, loopback, link-local,
		// documentation, multicast, reserved, broadcast.
		"0.0.0.0",
		"10.0.0.1",
		"100.64.0.1",
		"127.0.0.1",
		"169.254.169.254",
		"172.16.0.1",
		"192.0.0.1",
		"192.0.2.1",
		"192.168.1.1",
		"198.18.0.1",
		"198.51.100.7",
		"203.0.113.9",
		"224.0.0.1",
		"240.0.0.1",
		"255.255.255.255",
		// IPv6: unspecified, loopback, link-local, unique-local, multicast,
		// IPv4-mapped private, NAT64, 6to4, documentation, zone index.
		"::",
		"::1",
		"fe80::1",
		"fe80::1%eth0",
		"fc00::1",
		"fd00::1",
		"ff02::1",
		"::ffff:127.0.0.1",
		"64:ff9b::808:808",
		"2002::1",
		"2001:db8::1",
		"2001::1",
		"fec0::1",
		"100::1",
		"5f00::1",
		"3fff::1",
	}
	for _, raw := range nonPublic {
		addr := netip.MustParseAddr(raw)
		if isPublicAddress(addr) {
			t.Errorf("isPublicAddress(%s) = true, want false", raw)
		}
	}

	if isPublicAddress(netip.Addr{}) {
		t.Error("isPublicAddress(invalid) = true, want false")
	}
}

func TestResolvePublicAddresses(t *testing.T) {
	publicSet := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("8.8.4.4"),
			netip.MustParseAddr("2001:4860:4860::8888"),
		}, nil
	}

	t.Run("retains a fully public answer set", func(t *testing.T) {
		addrs, err := resolvePublicAddresses(t.Context(), "example.test", publicSet)
		if err != nil {
			t.Fatalf("resolvePublicAddresses: %v", err)
		}
		if len(addrs) != 2 {
			t.Fatalf("addresses = %v, want 2", addrs)
		}
	})

	t.Run("rejects the whole set when one address is not public", func(t *testing.T) {
		mixed := func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("127.0.0.1"),
			}, nil
		}
		_, err := resolvePublicAddresses(t.Context(), "rebinding.test", mixed)
		wantCode(t, err, codeBlockedURL)
	})

	t.Run("rejects an empty answer set", func(t *testing.T) {
		empty := func(context.Context, string) ([]netip.Addr, error) {
			return nil, nil
		}
		_, err := resolvePublicAddresses(t.Context(), "empty.test", empty)
		wantCode(t, err, codeProviderError)
	})

	t.Run("rejects an invalid answer", func(t *testing.T) {
		invalid := func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{{}}, nil
		}
		_, err := resolvePublicAddresses(t.Context(), "invalid.test", invalid)
		wantCode(t, err, codeProviderError)
	})

	t.Run("propagates a resolver failure", func(t *testing.T) {
		boom := errors.New("dns failed")
		failing := func(context.Context, string) ([]netip.Addr, error) {
			return nil, boom
		}
		_, err := resolvePublicAddresses(t.Context(), "broken.test", failing)
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want %v", err, boom)
		}
	})

	t.Run("checks IP literals without resolving", func(t *testing.T) {
		called := false
		unused := func(context.Context, string) ([]netip.Addr, error) {
			called = true
			return nil, errors.New("lookup must not run for a literal")
		}

		addrs, err := resolvePublicAddresses(t.Context(), "8.8.8.8", unused)
		if err != nil {
			t.Fatalf("public literal: %v", err)
		}
		if len(addrs) != 1 || addrs[0] != netip.MustParseAddr("8.8.8.8") {
			t.Errorf("addresses = %v, want [8.8.8.8]", addrs)
		}

		// Bracketed IPv6 literals keep their URL spelling until checked.
		if _, err := resolvePublicAddresses(t.Context(), "[2001:4860:4860::8888]", unused); err != nil {
			t.Fatalf("bracketed literal: %v", err)
		}

		if _, err := resolvePublicAddresses(t.Context(), "127.0.0.1", unused); err == nil {
			t.Fatal("private literal resolved, want a blocked error")
		}

		if called {
			t.Error("lookup ran for an IP literal")
		}
	})
}

func TestPinnedDialRefusesEmptyAddressSet(t *testing.T) {
	dial := pinnedDial(nil)
	if _, err := dial(t.Context(), "tcp", "example.com:80"); err == nil {
		t.Fatal("pinnedDial(nil) connected, want an error")
	}
}
