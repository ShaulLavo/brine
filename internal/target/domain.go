package target

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// CanonicalDomain removes scheme, port, case and a terminal DNS dot from live
// Caddy addresses. Only HTTP(S) addresses without credentials or paths are
// accepted. Wildcards and catch-all addresses remain explicit ownership claims.
func CanonicalDomain(address string) (string, error) {
	if address == "" || strings.TrimSpace(address) != address {
		return "", fmt.Errorf("invalid live domain")
	}
	authority := address
	if strings.Contains(address, "://") {
		u, e := url.Parse(address)
		if e != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(address, "#") {
			return "", fmt.Errorf("invalid live domain address")
		}
		authority = u.Host
	}
	literal := authority
	if strings.HasPrefix(literal, "[") && strings.HasSuffix(literal, "]") {
		literal = literal[1 : len(literal)-1]
	}
	if ip, e := netip.ParseAddr(literal); e == nil {
		return ip.String(), nil
	}
	host := authority
	if strings.Contains(authority, ":") {
		var port string
		var e error
		host, port, e = net.SplitHostPort(authority)
		if e != nil {
			return "", fmt.Errorf("invalid live domain port")
		}
		number, e := strconv.ParseUint(port, 10, 16)
		if e != nil || number == 0 {
			return "", fmt.Errorf("invalid live domain port")
		}
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" && strings.HasPrefix(authority, ":") {
		return "*", nil
	}
	if host == "*" {
		return host, nil
	}
	if ip, e := netip.ParseAddr(host); e == nil {
		return ip.String(), nil
	}
	name := strings.TrimPrefix(host, "*.")
	if len(name) == 0 || len(name) > 253 {
		return "", fmt.Errorf("invalid live domain name")
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid live domain label")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("invalid live domain label")
			}
		}
	}
	return host, nil
}
