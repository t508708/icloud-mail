// Package autologincode fetches a two-factor code without exposing provider secrets.
package autologincode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"icloud-api/internal/netproxy"
)

var (
	ErrNoCode          = errors.New("AUTO_LOGIN_CODE_PENDING")
	ErrInvalidResponse = errors.New("AUTO_LOGIN_CODE_INVALID_RESPONSE")
	ErrUnavailable     = errors.New("AUTO_LOGIN_CODE_UNAVAILABLE")
	ErrInvalidURL      = errors.New("AUTO_LOGIN_CODE_INVALID_URL")
)

const maxResponseBytes = 64 << 10

type Result struct {
	Code        string
	ReceivedAt  *time.Time
	Fingerprint string
}

type Client struct {
	httpClient *http.Client
}

type proxyContextKey struct{}

// WithProxy routes a code-provider fetch through the account's proxy chain.
func WithProxy(ctx context.Context, config netproxy.Config) context.Context {
	return context.WithValue(ctx, proxyContextKey{}, config)
}

func NewClient() *Client {
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            publicDialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  8 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		DisableKeepAlives:      true,
	}
	return &Client{httpClient: &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// ValidateURL validates syntax without contacting the provider or resolving DNS.
// DNS destinations are checked again at the actual connection boundary.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Host == "" || strings.TrimSpace(raw) != raw {
		return ErrInvalidURL
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "%\\\t\r\n ") || strings.HasSuffix(u.Host, ":") {
		return ErrInvalidURL
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ErrInvalidURL
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !isPublicIP(ip) {
			return ErrInvalidURL
		}
		return nil
	}
	if strings.ContainsAny(host, ":[]") || len(host) > 253 {
		return ErrInvalidURL
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ErrInvalidURL
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return ErrInvalidURL
			}
		}
	}
	return nil
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func isPublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	// Only the currently allocated global IPv6 unicast space is routable here.
	return ip.Is4() || netip.MustParsePrefix("2000::/3").Contains(ip)
}

func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 8 * time.Second}
	return dialPublic(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func dialPublic(ctx context.Context, network, address string, lookup func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalidURL
	}
	ips, err := lookup(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, ErrUnavailable
	}
	// Reject mixed public/private DNS answers, not merely the first address.
	for index, ip := range ips {
		// LookupNetIP wraps net.IP and can represent an ordinary A record as
		// IPv4-mapped IPv6. Classify and pin its canonical IPv4 destination.
		ips[index] = ip.Unmap()
		if !isPublicIP(ips[index]) {
			return nil, ErrInvalidURL
		}
	}
	for _, ip := range ips {
		conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, ErrUnavailable
}

func (c *Client) Fetch(ctx context.Context, rawURL string) (Result, error) {
	if ValidateURL(rawURL) != nil {
		return Result{}, ErrInvalidURL
	}
	if c == nil || c.httpClient == nil {
		return Result{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Result{}, ErrInvalidURL
	}
	req.Header.Set("Accept", "application/json, text/plain")
	client := c.httpClient
	if proxy, ok := ctx.Value(proxyContextKey{}).(netproxy.Config); ok {
		if err := proxy.Validate(); err != nil {
			return Result{}, ErrUnavailable
		}
		transport := &http.Transport{
			Proxy: nil,
			DialContext: func(dialCtx context.Context, network, address string) (net.Conn, error) {
				return dialPublic(dialCtx, network, address, net.DefaultResolver.LookupNetIP, proxy.DialContext)
			},
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  8 * time.Second,
			MaxResponseHeaderBytes: 16 << 10,
			DisableKeepAlives:      true,
		}
		defer transport.CloseIdleConnections()
		client = &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrInvalidURL) {
			return Result{}, ErrInvalidURL
		}
		return Result{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return Result{}, ErrNoCode
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, ErrUnavailable
	}
	if len(body) > maxResponseBytes {
		return Result{}, ErrInvalidResponse
	}
	result, err := parseResponse(body)
	if err != nil {
		return Result{}, err
	}
	stamp := ""
	if result.ReceivedAt != nil {
		stamp = result.ReceivedAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(result.Code + "\n" + stamp))
	result.Fingerprint = hex.EncodeToString(sum[:])
	return result, nil
}
