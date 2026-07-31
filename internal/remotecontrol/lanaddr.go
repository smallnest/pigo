package remotecontrol

import (
	"errors"
	"fmt"
	"net"
)

// ErrNoLAN is returned by DetectRoutableIP when no routable, non-loopback IPv4
// address can be found. Binding loopback would produce a URL the phone cannot
// reach, so the caller must surface this rather than fall back silently.
var ErrNoLAN = errors.New("remotecontrol: no routable LAN address found; are you connected to Wi-Fi?")

// addrLister returns the machine's network addresses. It is a package variable
// so tests can inject a fake set of interfaces without real hardware.
var addrLister = net.InterfaceAddrs

// DetectRoutableIP returns the first routable, non-loopback IPv4 address of the
// host, suitable for embedding in the printed pairing URL. On a multi-NIC host
// it returns the first match; callers that need a specific interface should
// override via Config.Host. It returns ErrNoLAN when nothing routable exists.
func DetectRoutableIP() (string, error) {
	addrs, err := addrLister()
	if err != nil {
		return "", fmt.Errorf("remotecontrol: list interfaces: %w", err)
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		v4 := ip.To4()
		if v4 == nil {
			continue // skip IPv6 for the LAN URL
		}
		if v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsLinkLocalMulticast() || v4.IsUnspecified() {
			continue
		}
		return v4.String(), nil
	}
	return "", ErrNoLAN
}

// ListenFreePort binds a TCP listener on host. It first tries the requested
// port; if port is 0 or already in use it falls back to a kernel-assigned free
// port (:0). It returns the listener and the actual port bound.
func ListenFreePort(host string, port int) (net.Listener, int, error) {
	if port != 0 {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
		if err == nil {
			return ln, ln.Addr().(*net.TCPAddr).Port, nil
		}
		// Requested port unavailable — fall through to an auto-assigned one.
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, 0, fmt.Errorf("remotecontrol: bind %s: %w", host, err)
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}
