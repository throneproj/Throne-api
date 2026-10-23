package web

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// DefaultListen is the address the gateway listens on by default.
const DefaultListen = "127.0.0.1:9096"

// ParseListen parses a listen address in the forms Throne's --api-listen takes: PORT,
// IPv4:PORT, [IPv6]:PORT and *:PORT, which listens on every address. PORT alone means
// 127.0.0.1. It returns the address for net.Listen and whether it is a loopback one.
func ParseListen(text string) (addr string, loopback bool, err error) {
	value := strings.TrimSpace(text)
	host, portText := "127.0.0.1", value
	colon := strings.LastIndexByte(value, ':')
	if colon >= 0 {
		host, portText = value[:colon], value[colon+1:]
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 || len(portText) > 5 || strings.ContainsAny(portText, "+-") {
		return "", false, fmt.Errorf("%q needs a port from 1 to 65535: PORT, IP:PORT, [IPv6]:PORT or *:PORT", value)
	}
	switch {
	case host == "" || host == "*":
		return ":" + strconv.Itoa(port), false, nil
	case strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]"):
		ip, err := netip.ParseAddr(host[1 : len(host)-1])
		if err != nil || !ip.Is6() || ip.Zone() != "" {
			return "", false, fmt.Errorf("%q is not an IPv6 address", host)
		}
		host = ip.String()
	case strings.Contains(host, ":"):
		return "", false, fmt.Errorf("%q: write an IPv6 address in brackets, as in [::1]:9096", value)
	default:
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Is4() {
			return "", false, fmt.Errorf("%q is not an IP address; host names are not accepted", host)
		}
		host = ip.String()
	}
	ip := netip.MustParseAddr(host)
	return net.JoinHostPort(host, strconv.Itoa(port)), ip.IsLoopback(), nil
}

// loopbackListen says whether a net.Listen address is a loopback one.
func loopbackListen(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("listen address %q: %v", addr, err)
	}
	if host == "" {
		return false, nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false, fmt.Errorf("listen address %q: %q is not an IP address", addr, host)
	}
	return ip.IsLoopback(), nil
}

// serveURLs are the URLs that reach a listener: its address, or for one on every
// address, localhost and the addresses of the network interfaces.
func serveURLs(addr net.Addr, tls bool) []string {
	scheme := "http"
	if tls {
		scheme = "https"
	}
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return []string{scheme + "://" + addr.String() + "/"}
	}
	port := strconv.Itoa(int(ap.Port()))
	ip := ap.Addr().Unmap()
	if !ip.IsUnspecified() {
		return []string{scheme + "://" + net.JoinHostPort(ip.String(), port) + "/"}
	}
	urls := []string{scheme + "://localhost:" + port + "/"}
	for _, a := range interfaceAddrs(ip.Is4()) {
		urls = append(urls, scheme+"://"+net.JoinHostPort(a.String(), port)+"/")
	}
	return urls
}

// interfaceAddrs returns the addresses of the network interfaces that are neither
// loopback nor link-local; only IPv4 ones with only4.
func interfaceAddrs(only4 bool) []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || (only4 && !ip.Is4()) {
			continue
		}
		out = append(out, ip)
	}
	return out
}

// allowList normalizes the host names of --allow-host.
func allowList(hosts []string) (map[string]bool, error) {
	allow := map[string]bool{}
	for _, h := range hosts {
		name := hostName(h)
		if name == "" || strings.ContainsAny(name, "/@?#") {
			return nil, fmt.Errorf("%q is not a host name", h)
		}
		allow[name] = true
	}
	return allow, nil
}

// hostName returns the host of a Host header value, lowercase, without the port, the
// brackets of an IPv6 address and a trailing dot.
func hostName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return strings.TrimSuffix(host, ".")
}

// hostAllowed applies the Host check: a loopback listener accepts the loopback names and
// the --allow-host names; another accepts every host unless --allow-host is given, and
// then those names, the loopback names and IP addresses, which DNS rebinding cannot use.
func (s *Server) hostAllowed(host string) bool {
	name := hostName(host)
	switch {
	case name == "":
		return !s.loopback && len(s.allow) == 0
	case s.allow[name], name == "localhost", name == "127.0.0.1", name == "::1":
		return true
	case s.loopback:
		return false
	case len(s.allow) == 0:
		return true
	}
	_, err := netip.ParseAddr(name)
	return err == nil
}
