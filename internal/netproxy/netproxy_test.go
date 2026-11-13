package netproxy

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseFormsAndRedaction(t *testing.T) {
	tests := []struct{ raw, scheme, address, user, pass string }{
		{"proxy.example:8080", "http", "proxy.example:8080", "", ""},
		{"https://user:secret@proxy.example:443", "https", "proxy.example:443", "user", "secret"},
		{"socks5://user:secret@proxy.example:1080", "socks5", "proxy.example:1080", "user", "secret"},
		{"user:secret@proxy.example:8080", "http", "proxy.example:8080", "user", "secret"},
		{"proxy.example:8080@user:secret", "http", "proxy.example:8080", "user", "secret"},
		{"user:12345@proxy.example:8080", "http", "proxy.example:8080", "user", "12345"},
		{"proxy.example:8080@user:12345", "http", "proxy.example:8080", "user", "12345"},
		{"user:12345@proxy:8080", "http", "proxy:8080", "user", "12345"},
		{"proxy:8080@user:12345", "http", "proxy:8080", "user", "12345"},
		{"proxy.example:8080:user:secret", "http", "proxy.example:8080", "user", "secret"},
	}
	for _, tt := range tests {
		e, err := Parse(tt.raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.raw, err)
		}
		if e.scheme != tt.scheme || e.address != tt.address || e.username != tt.user || e.password != tt.pass {
			t.Errorf("Parse(%q) = %#v", tt.raw, e)
		}
		if strings.Contains(e.String(), "secret") {
			t.Errorf("credentials leaked in String: %q", e.String())
		}
	}
	for _, raw := range []string{"socks4://host:1080", "http://u:secret@host", "http://user@host:80", "host:70000", "bad\r\nHost: wrong:80"} {
		_, err := Parse(raw)
		if err == nil {
			t.Errorf("Parse(%q) succeeded", raw)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("credential leaked in error: %v", err)
		}
	}
}

func TestHTTPConnect(t *testing.T) {
	proxy, seen := startHTTPProxy(t, false, "", "")
	assertTunnelEcho(t, Config{Endpoint: proxy}, seen, "hello")
}

func TestHTTPConnectWithAuthentication(t *testing.T) {
	proxy, seen := startHTTPProxy(t, false, "user", "secret")
	assertTunnelEcho(t, Config{Endpoint: strings.Replace(proxy, "://", "://user:secret@", 1)}, seen, "auth")
}

func TestTLSConnect(t *testing.T) {
	cert := testCertificate(t)
	proxy, seen := startHTTPProxyWithCert(t, &cert, "", "")
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)
	assertTunnelEcho(t, Config{Endpoint: proxy, TLSConfig: &tls.Config{RootCAs: pool}}, seen, "tls")
}

func TestSOCKS5(t *testing.T) {
	proxy, seen := startSOCKS5Proxy(t, "", "")
	assertTunnelEcho(t, Config{Endpoint: proxy}, seen, "socks")
}

func TestSOCKS5WithAuthentication(t *testing.T) {
	proxy, seen := startSOCKS5Proxy(t, "user", "secret")
	assertTunnelEcho(t, Config{Endpoint: proxy}, seen, "socks-auth")
}

func TestProxyFailureIdentifiesHopAndAuthentication(t *testing.T) {
	endpoint, _ := startHTTPProxy(t, false, "user", "correct")
	upstream, _ := startHTTPProxy(t, false, "front", "correct")
	socks, _ := startSOCKS5Proxy(t, "socks", "correct")
	for _, tt := range []struct {
		name, endpoint, upstream, hop string
	}{
		{"exit HTTP", strings.Replace(endpoint, "http://", "http://user:wrong@", 1), "", "endpoint"},
		{"front HTTP", endpoint, strings.Replace(upstream, "http://", "http://front:wrong@", 1), "upstream"},
		{"exit SOCKS5", strings.Replace(socks, "socks5://", "socks5://socks:wrong@", 1), "", "endpoint"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := (Config{Endpoint: tt.endpoint, Upstream: tt.upstream}).DialContext(ctx, "tcp", "icloud.example:443")
			if conn != nil {
				_ = conn.Close()
			}
			var failure *ConnectError
			if !errors.As(err, &failure) || failure.Hop != tt.hop || failure.Stage != "authentication" || !errors.Is(err, ErrAuthentication) {
				t.Fatalf("proxy error = %v, want %s authentication failure", err, tt.hop)
			}
			if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), "correct") {
				t.Fatal("proxy password appeared in error")
			}
		})
	}
}

func TestChainedMixedProxy(t *testing.T) {
	endpoint, endpointSeen := startHTTPProxy(t, false, "", "")
	upstream, upstreamSeen := startSOCKS5Proxy(t, "", "")
	assertTunnelEcho(t, Config{Endpoint: endpoint, Upstream: upstream}, endpointSeen, "chain")
	if got := <-upstreamSeen; got != strings.TrimPrefix(endpoint, "http://") {
		t.Fatalf("upstream target = %q", got)
	}
}

func TestChainedTLSProxyWithAuthentication(t *testing.T) {
	cert := testCertificate(t)
	endpoint, endpointSeen := startHTTPProxyWithCert(t, &cert, "user", "secret")
	upstream, upstreamSeen := startSOCKS5Proxy(t, "front", "password")
	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)
	config := Config{
		Endpoint:  strings.Replace(endpoint, "://", "://user:secret@", 1),
		Upstream:  upstream,
		TLSConfig: &tls.Config{RootCAs: pool},
	}
	assertTunnelEcho(t, config, endpointSeen, "tls-chain")
	if target := <-upstreamSeen; target != strings.TrimPrefix(endpoint, "https://") {
		t.Fatalf("upstream target = %q", target)
	}
}

func assertTunnelEcho(t *testing.T, cfg Config, seen <-chan string, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, err := cfg.DialContext(ctx, "tcp", "target.test:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	if target := <-seen; target != "target.test:443" {
		t.Fatalf("target = %q", target)
	}
}

func startTarget() (string, <-chan struct{}, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	return ln.Addr().String(), done, nil
}

func startHTTPProxy(t *testing.T, tlsProxy bool, username, password string) (string, <-chan string) {
	t.Helper()
	var cert tls.Certificate
	if tlsProxy {
		cert = testCertificate(t)
	}
	return startHTTPProxyWithCert(t, func() *tls.Certificate {
		if tlsProxy {
			return &cert
		}
		return nil
	}(), username, password)
}

func startHTTPProxyWithCert(t *testing.T, cert *tls.Certificate, username, password string) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if cert != nil {
		listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12})
	}
	seen := make(chan string, 1)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				if username != "" && req.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)) {
					_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
					return
				}
				target, done, err := startTarget()
				if err != nil {
					return
				}
				defer func() { <-done }()
				seen <- req.Host
				up, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
				go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
				_, _ = io.Copy(c, up)
				_ = c.Close()
			}(client)
		}
	}()
	prefix := "http://"
	if cert != nil {
		prefix = "https://"
	}
	return prefix + listener.Addr().String(), seen
}

func startSOCKS5Proxy(t *testing.T, username, password string) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	t.Cleanup(func() { _ = ln.Close() })
	localTarget, _, err := startTarget()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var h [2]byte
				if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 5 {
					return
				}
				methods := make([]byte, int(h[1]))
				if _, err := io.ReadFull(c, methods); err != nil {
					return
				}
				method := byte(0)
				if username != "" {
					method = 2
				}
				found := false
				for _, offered := range methods {
					if offered == method {
						found = true
					}
				}
				if !found {
					_, _ = c.Write([]byte{5, 0xff})
					return
				}
				_, _ = c.Write([]byte{5, method})
				var n [1]byte
				if method == 2 {
					var a [2]byte
					if _, err := io.ReadFull(c, a[:]); err != nil {
						return
					}
					u := make([]byte, int(a[1]))
					_, _ = io.ReadFull(c, u)
					_, _ = io.ReadFull(c, n[:])
					p := make([]byte, int(n[0]))
					_, _ = io.ReadFull(c, p)
					if string(u) != username || string(p) != password {
						_, _ = c.Write([]byte{1, 1})
						return
					}
					_, _ = c.Write([]byte{1, 0})
				}
				var q [4]byte
				if _, err := io.ReadFull(c, q[:]); err != nil {
					return
				}
				var host string
				switch q[3] {
				case 1:
					b := make([]byte, 4)
					_, _ = io.ReadFull(c, b)
					host = net.IP(b).String()
				case 4:
					b := make([]byte, 16)
					_, _ = io.ReadFull(c, b)
					host = net.IP(b).String()
				case 3:
					_, _ = io.ReadFull(c, n[:])
					b := make([]byte, int(n[0]))
					_, _ = io.ReadFull(c, b)
					host = string(b)
				default:
					return
				}
				var pb [2]byte
				_, _ = io.ReadFull(c, pb[:])
				target := net.JoinHostPort(host, strconv.Itoa(int(pb[0])<<8|int(pb[1])))
				seen <- target
				if host == "target.test" {
					target = localTarget
				}
				up, err := net.Dial("tcp", target)
				if err != nil {
					_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				_, _ = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}(client)
		}
	}()
	if username == "" {
		return "socks5://" + ln.Addr().String(), seen
	}
	return "socks5://" + username + ":" + password + "@" + ln.Addr().String(), seen
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, DNSNames: []string{"127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
