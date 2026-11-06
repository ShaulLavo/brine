package readiness

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

var errProbe = errors.New("readiness probe unavailable")

// NetworkProbes uses no proxy environment, ambient credentials or redirect targets.
// The only HTTP path is /v2/ and TLS always verifies system roots and the SNI name.
type NetworkProbes struct {
	lookup func(context.Context, string, string) ([]net.IP, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	roots  *x509.CertPool // nil selects system roots; tests supply an isolated fixture CA.
}

func validExactDomain(name string) bool {
	canonical, err := target.CanonicalDomain(name)
	return err == nil && canonical == name && !strings.Contains(name, "*")
}
func validRegistryHost(host string) bool {
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || !validExactDomain(u.Hostname()) {
		return false
	}
	if u.Port() == "" {
		return !strings.HasSuffix(host, ":")
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	return err == nil && port > 0
}
func (n NetworkProbes) Registry(ctx context.Context, host string) (bool, error) {
	// Match the existing OCI resolver's fixed Docker Hub API origin, not a redirect.
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
	}
	u := url.URL{Scheme: "https", Host: host, Path: "/v2/"}
	if !validRegistryHost(host) {
		return false, errProbe
	}
	transport := &http.Transport{Proxy: nil, DialContext: n.dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: n.roots}, DisableKeepAlives: true, MaxResponseHeaderBytes: 8 << 10, ResponseHeaderTimeout: ProbeTimeout}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: ProbeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, errProbe
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusUnauthorized, nil
}
func (n NetworkProbes) DNS(ctx context.Context, name string, local target.Observation[[]string]) (string, error) {
	if !validExactDomain(name) {
		return "", errProbe
	}
	lookup := n.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIP
	}
	addresses, err := lookup(ctx, "ip", name)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			return "unresolved", nil
		}
		return "", errProbe
	}
	if len(addresses) == 0 {
		return "unresolved", nil
	}
	if len(addresses) > 64 || local.Value == nil {
		return "", errProbe
	}
	return dnsLocation(addresses, *local.Value)
}
func dnsLocation(addresses []net.IP, local []string) (string, error) {
	if len(local) == 0 {
		return "", errProbe
	}
	here := map[netip.Addr]bool{}
	public := false
	for _, text := range local {
		ip, err := netip.ParseAddr(text)
		if err != nil {
			return "", errProbe
		}
		ip = ip.Unmap()
		here[ip] = true
		public = public || ip.IsGlobalUnicast() && !ip.IsPrivate()
	}
	allHere := true
	for _, ip := range addresses {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok {
			return "", errProbe
		}
		allHere = allHere && here[addr.Unmap()]
	}
	if allHere {
		return "resolves_here", nil
	}
	if !public {
		return "unknown_behind_nat", nil
	}
	return "resolves_elsewhere", nil
}
func (n NetworkProbes) TLS(ctx context.Context, name string) (string, error) {
	if !validExactDomain(name) {
		return "", errProbe
	}
	dial := n.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: ProbeTimeout}).DialContext
	}
	raw, err := dial(ctx, "tcp", "127.0.0.1:443")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "unreachable", nil
	}
	connection := tls.Client(raw, &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12, RootCAs: n.roots})
	defer func() { _ = connection.Close() }()
	if err := connection.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var verification *tls.CertificateVerificationError
		if errors.As(err, &verification) {
			return "untrusted", nil
		}
		return "unreachable", nil
	}
	return "trusted", nil
}
