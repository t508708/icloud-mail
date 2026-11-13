package netproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Endpoint is a parsed proxy endpoint. Credentials are kept private and never
// included in its string representation or errors.
type Endpoint struct {
	scheme   string
	address  string
	username string
	password string
}

// Config describes a final proxy and an optional proxy used to reach it.
type Config struct {
	Endpoint  string
	Upstream  string
	TLSConfig *tls.Config
}

var ErrAuthentication = errors.New("proxy authentication rejected")

// ConnectError identifies the proxy hop and the operation that failed.
// HTTP callers can report the stage without disclosing proxy credentials.
type ConnectError struct {
	Hop   string
	Stage string
	Err   error
}

func (e *ConnectError) Error() string {
	return fmt.Sprintf("%s proxy %s: %v", e.Hop, e.Stage, e.Err)
}

func (e *ConnectError) Unwrap() error { return e.Err }

func (e Endpoint) String() string { return e.scheme + "://" + e.address }

// Parse accepts URL form and common host/credential forms. Bare host:port uses
// HTTP; ambiguous colon-delimited credentials use host:port:username:password.
func Parse(raw string) (Endpoint, error) {
	bad := func() (Endpoint, error) { return Endpoint{}, errors.New("invalid proxy endpoint") }
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return bad()
	}
	scheme := "http"
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = strings.ToLower(raw[:i])
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || parsed.Port() == "" ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return bad()
		}
		username, password := "", ""
		if parsed.User != nil {
			username = parsed.User.Username()
			password, _ = parsed.User.Password()
			if username == "" || password == "" {
				return bad()
			}
		}
		if scheme != "http" && scheme != "https" && scheme != "socks5" {
			return bad()
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return bad()
		}
		return Endpoint{scheme: scheme, address: net.JoinHostPort(parsed.Hostname(), parsed.Port()), username: username, password: password}, nil
	}
	var username, password, hostport string
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		left, right := raw[:at], raw[at+1:]
		if strings.Contains(left, "://") || right == "" {
			return bad()
		}
		leftHost, rightHost := hostPortLike(left), hostPortLike(right)
		if leftHost && (!rightHost || proxyHostHint(left) && !proxyHostHint(right) ||
			!proxyHostHint(right) && commonProxyPort(left) && !commonProxyPort(right)) {
			hostport = left
			username, password, _ = strings.Cut(right, ":")
		} else if rightHost {
			hostport = right
			credential := strings.SplitN(left, ":", 2)
			if len(credential) != 2 {
				return bad()
			}
			username, password = credential[0], credential[1]
		} else {
			return bad()
		}
	} else {
		parts := strings.Split(raw, ":")
		if len(parts) == 4 {
			hostport = net.JoinHostPort(parts[0], parts[1])
			username, password = parts[2], parts[3]
		} else {
			hostport = raw
		}
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || host == "" || strings.ContainsAny(host, "/?#@") || strings.IndexFunc(host, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return bad()
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return bad()
	}
	if (username == "") != (password == "") {
		return bad()
	}
	return Endpoint{scheme: scheme, address: net.JoinHostPort(host, port), username: username, password: password}, nil
}

func hostPortLike(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && host != "" && p > 0 && p <= 65535
}

// Domain names, IP addresses and localhost distinguish a proxy host from a
// username paired with a numeric password in otherwise ambiguous @ forms.
func proxyHostHint(value string) bool {
	host, _, err := net.SplitHostPort(value)
	return err == nil && (net.ParseIP(host) != nil || strings.Contains(host, ".") || strings.EqualFold(host, "localhost"))
}

func commonProxyPort(value string) bool {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	switch port {
	case "80", "443", "1080", "3128", "8000", "8080", "8081", "8443", "8888":
		return true
	}
	return false
}

func (c Config) Validate() error {
	if _, err := Parse(c.Endpoint); err != nil {
		return err
	}
	if strings.TrimSpace(c.Upstream) != "" {
		if _, err := Parse(c.Upstream); err != nil {
			return errors.New("invalid upstream proxy endpoint")
		}
	}
	return nil
}

func (c Config) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && !strings.HasPrefix(network, "tcp") {
		return nil, errors.New("proxy supports TCP networks only")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint, err := Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	var upstream *Endpoint
	if strings.TrimSpace(c.Upstream) != "" {
		u, e := Parse(c.Upstream)
		if e != nil {
			return nil, errors.New("invalid upstream proxy endpoint")
		}
		upstream = &u
	}
	dialer := net.Dialer{}
	var conn net.Conn
	if upstream == nil {
		conn, err = dialer.DialContext(ctx, "tcp", endpoint.address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", upstream.address)
	}
	if err != nil {
		hop := "endpoint"
		if upstream != nil {
			hop = "upstream"
		}
		return nil, &ConnectError{Hop: hop, Stage: "connect", Err: err}
	}
	baseConn := conn
	stop := context.AfterFunc(ctx, func() { _ = baseConn.Close() })
	defer stop()
	failed := true
	defer func() {
		if failed {
			_ = baseConn.Close()
		}
	}()
	if upstream == nil {
		conn, err = wrapProxyTLS(ctx, conn, endpoint, c.TLSConfig)
		if err != nil {
			return nil, &ConnectError{Hop: "endpoint", Stage: "tls", Err: err}
		}
		conn, err = handshake(ctx, conn, endpoint, address)
		if err != nil {
			return nil, proxyHandshakeError("endpoint", err)
		}
	} else {
		conn, err = wrapProxyTLS(ctx, conn, *upstream, c.TLSConfig)
		if err != nil {
			return nil, &ConnectError{Hop: "upstream", Stage: "tls", Err: err}
		}
		conn, err = handshake(ctx, conn, *upstream, endpoint.address)
		if err != nil {
			return nil, proxyHandshakeError("upstream", err)
		}
		conn, err = wrapProxyTLS(ctx, conn, endpoint, c.TLSConfig)
		if err != nil {
			return nil, &ConnectError{Hop: "endpoint", Stage: "tls", Err: err}
		}
		conn, err = handshake(ctx, conn, endpoint, address)
		if err != nil {
			return nil, proxyHandshakeError("endpoint", err)
		}
	}
	failed = false
	return conn, nil
}

func proxyHandshakeError(hop string, err error) error {
	stage := "tunnel"
	if errors.Is(err, ErrAuthentication) {
		stage = "authentication"
	}
	return &ConnectError{Hop: hop, Stage: stage, Err: err}
}

func wrapProxyTLS(ctx context.Context, conn net.Conn, endpoint Endpoint, custom *tls.Config) (net.Conn, error) {
	if endpoint.scheme != "https" {
		return conn, nil
	}
	serverName, _, _ := net.SplitHostPort(endpoint.address)
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if custom != nil {
		cfg = custom.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = serverName
		}
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
	}
	tlsConn := tls.Client(conn, cfg)
	if deadline, ok := ctx.Deadline(); ok {
		_ = tlsConn.SetDeadline(deadline)
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, errors.New("proxy TLS handshake failed")
	}
	_ = tlsConn.SetDeadline(time.Time{})
	return tlsConn, nil
}

func handshake(ctx context.Context, conn net.Conn, proxy Endpoint, target string) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	var err error
	switch proxy.scheme {
	case "http", "https":
		conn, err = httpConnect(conn, proxy, target)
	case "socks5":
		err = socks5Connect(conn, proxy, target)
	default:
		err = errors.New("unsupported proxy scheme")
	}
	if conn != nil {
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, err
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func httpConnect(conn net.Conn, proxy Endpoint, target string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(target); err != nil {
		return nil, errors.New("invalid target address")
	}
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if proxy.username != "" {
		token := base64.StdEncoding.EncodeToString([]byte(proxy.username + ":" + proxy.password))
		req += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		return nil, errors.New("proxy CONNECT write failed")
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, errors.New("proxy CONNECT response invalid")
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusProxyAuthRequired {
			return nil, ErrAuthentication
		}
		return nil, fmt.Errorf("proxy CONNECT rejected with status %d", resp.StatusCode)
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

func socks5Connect(conn net.Conn, proxy Endpoint, target string) error {
	methods := []byte{0}
	if proxy.username != "" {
		methods = []byte{2}
	}
	if _, err := conn.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return errors.New("SOCKS5 greeting failed")
	}
	var choice [2]byte
	if _, err := io.ReadFull(conn, choice[:]); err != nil || choice[0] != 5 || choice[1] == 0xff {
		return fmt.Errorf("%w: SOCKS5 method", ErrAuthentication)
	}
	if choice[1] == 2 && proxy.username != "" {
		if len(proxy.username) > 255 || len(proxy.password) > 255 {
			return errors.New("SOCKS5 credentials invalid")
		}
		msg := append([]byte{1, byte(len(proxy.username))}, []byte(proxy.username)...)
		msg = append(msg, byte(len(proxy.password)))
		msg = append(msg, []byte(proxy.password)...)
		if _, err := conn.Write(msg); err != nil {
			return errors.New("SOCKS5 authentication failed")
		}
		var auth [2]byte
		if _, err := io.ReadFull(conn, auth[:]); err != nil || auth[1] != 0 {
			return fmt.Errorf("%w: SOCKS5 credentials", ErrAuthentication)
		}
	} else if choice[1] != 0 || proxy.username != "" {
		return fmt.Errorf("%w: SOCKS5 method mismatch", ErrAuthentication)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("invalid target address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid target port")
	}
	var req []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append([]byte{5, 1, 0, 1}, v4...)
		} else {
			req = append([]byte{5, 1, 0, 4}, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("target hostname too long")
		}
		req = append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return errors.New("SOCKS5 CONNECT failed")
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil || head[0] != 5 || head[1] != 0 {
		return errors.New("SOCKS5 CONNECT rejected")
	}
	n := 0
	switch head[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return errors.New("SOCKS5 response invalid")
		}
		n = int(size[0])
	default:
		return errors.New("SOCKS5 response invalid")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(n+2)); err != nil {
		return errors.New("SOCKS5 response invalid")
	}
	return nil
}

func (c Config) HTTPTransport() (*http.Transport, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.TLSConfig != nil {
		tlsConfig = c.TLSConfig.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           c.DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       tlsConfig,
		MaxConnsPerHost:       4,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}, nil
}
