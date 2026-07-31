package remotecontrol

import (
	"net"
	"testing"
)

// withAddrLister swaps the package addrLister for the duration of a test.
func withAddrLister(t *testing.T, fn func() ([]net.Addr, error)) {
	t.Helper()
	orig := addrLister
	addrLister = fn
	t.Cleanup(func() { addrLister = orig })
}

func TestDetectRoutableIPPicksRoutableV4(t *testing.T) {
	withAddrLister(t, func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.IPv6loopback},
			&net.IPNet{IP: net.ParseIP("127.0.0.1")},
			&net.IPNet{IP: net.ParseIP("169.254.1.5")}, // link-local
			&net.IPNet{IP: net.ParseIP("192.168.1.42")},
			&net.IPNet{IP: net.ParseIP("10.0.0.9")},
		}, nil
	})
	ip, err := DetectRoutableIP()
	if err != nil {
		t.Fatalf("DetectRoutableIP: %v", err)
	}
	if ip != "192.168.1.42" {
		t.Fatalf("ip = %q, want 192.168.1.42 (first routable v4)", ip)
	}
}

func TestDetectRoutableIPSkipsLoopbackAndLinkLocal(t *testing.T) {
	withAddrLister(t, func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1")},
			&net.IPNet{IP: net.ParseIP("169.254.1.5")},
		}, nil
	})
	if _, err := DetectRoutableIP(); err != ErrNoLAN {
		t.Fatalf("err = %v, want ErrNoLAN", err)
	}
}

func TestDetectRoutableIPNoInterfaces(t *testing.T) {
	withAddrLister(t, func() ([]net.Addr, error) {
		return nil, nil
	})
	if _, err := DetectRoutableIP(); err != ErrNoLAN {
		t.Fatalf("err = %v, want ErrNoLAN", err)
	}
}

func TestListenFreePortAutoAssign(t *testing.T) {
	ln, port, err := ListenFreePort("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("ListenFreePort: %v", err)
	}
	defer ln.Close()
	if port <= 0 {
		t.Fatalf("port = %d, want > 0", port)
	}
	if got := ln.Addr().(*net.TCPAddr).Port; got != port {
		t.Fatalf("listener port %d != returned %d", got, port)
	}
}

func TestListenFreePortFallsBackWhenOccupied(t *testing.T) {
	// Occupy a port, then ask ListenFreePort for that same port; it must fall
	// back to a different, free one instead of failing.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer occupied.Close()
	busyPort := occupied.Addr().(*net.TCPAddr).Port

	ln, port, err := ListenFreePort("127.0.0.1", busyPort)
	if err != nil {
		t.Fatalf("ListenFreePort: %v", err)
	}
	defer ln.Close()
	if port == busyPort {
		t.Fatalf("port = %d, expected fallback away from occupied %d", port, busyPort)
	}
}

func TestListenFreePortUsesRequestedWhenFree(t *testing.T) {
	// Find a free port, release it, then request it explicitly.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ln, port, err := ListenFreePort("127.0.0.1", want)
	if err != nil {
		t.Fatalf("ListenFreePort: %v", err)
	}
	defer ln.Close()
	if port != want {
		t.Fatalf("port = %d, want requested %d", port, want)
	}
}
