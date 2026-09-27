package collect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/heliowap/vpsdash/internal/config"
)

type healthPolicy struct {
	inventory map[string]bool
	lookup    func(context.Context, string) ([]net.IPAddr, error)
}

var errHealthURLDenied = errors.New("health URL targets an unapproved address")
var errHealthDNSUnavailable = errors.New("health URL host could not be resolved")

var nonPublicRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func newHealthPolicy(hosts []config.Host) *healthPolicy {
	p := &healthPolicy{inventory: map[string]bool{}, lookup: net.DefaultResolver.LookupIPAddr}
	for _, host := range hosts {
		p.inventory[normalizedHost(host.TailnetName)] = true
	}
	return p
}

func normalizedHost(host string) string { return strings.ToLower(strings.TrimSuffix(host, ".")) }

func (p *healthPolicy) addresses(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	return p.lookup(ctx, host)
}

func (p *healthPolicy) allowedIP(host string, ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if p.inventory[host] {
		// A loopback address is accepted only if the operator explicitly put
		// that literal address in the inventory, never through DNS rebinding.
		return !ip.IsLoopback() || host == ip.String()
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	for _, prefix := range nonPublicRanges {
		if prefix.Contains(addr.Unmap()) {
			return false
		}
	}
	return true
}

func (p *healthPolicy) allowedAddresses(ctx context.Context, host string) ([]net.IPAddr, error) {
	host = normalizedHost(host)
	addresses, err := p.addresses(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errHealthDNSUnavailable, err)
	}
	if len(addresses) == 0 {
		return nil, errHealthDNSUnavailable
	}
	for _, address := range addresses {
		if !p.allowedIP(host, address.IP) {
			return nil, errHealthURLDenied
		}
	}
	return addresses, nil
}

func parseHealthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid health URL")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("invalid health URL port")
		}
	}
	return u, nil
}

func (p *healthPolicy) validate(ctx context.Context, raw string) error {
	u, err := parseHealthURL(raw)
	if err != nil {
		return err
	}
	_, err = p.allowedAddresses(ctx, u.Hostname())
	return err
}

// ValidateHealthURL rejects malformed or known-unapproved destinations at
// save time. DNS failures are retried by the collector at probe time.
func ValidateHealthURL(ctx context.Context, hosts []config.Host, raw string) error {
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return newHealthPolicy(hosts).validateForSave(lookupCtx, raw)
}

func (p *healthPolicy) validateForSave(ctx context.Context, raw string) error {
	err := p.validate(ctx, raw)
	if errors.Is(err, errHealthDNSUnavailable) {
		return nil // The probe rechecks the destination when DNS is available.
	}
	return err
}

func newHealthHTTPClient(hosts []config.Host) *http.Client {
	return newHealthHTTPClientWithPolicy(newHealthPolicy(hosts))
}

func newHealthHTTPClientWithPolicy(policy *healthPolicy) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		MaxIdleConns:    16,
		IdleConnTimeout: 90 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := policy.allowedAddresses(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, candidate := range addresses {
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				err = dialErr
			}
			return nil, fmt.Errorf("health URL connection failed: %w", err)
		},
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || (len(via) > 0 && normalizedHost(req.URL.Hostname()) != normalizedHost(via[0].URL.Hostname())) {
				return fmt.Errorf("%w: redirect changed host or exceeded limit", errHealthURLDenied)
			}
			return policy.validate(req.Context(), req.URL.String())
		},
	}
}
