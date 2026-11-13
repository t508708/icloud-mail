package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

func TestAccountSettingsDialUsesAccountProxy(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	connected := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connected <- r.Header.Get("Proxy-Authorization")
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		upstream, err := net.Dial("tcp", r.Host)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("proxy does not support hijacking")
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack proxy: %v", err)
			return
		}
		defer client.Close()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		go func() { _, _ = io.Copy(upstream, buffered) }()
		_, _ = io.Copy(client, upstream)
	}))
	defer proxyServer.Close()
	endpoint := "http://user:pass@" + strings.TrimPrefix(proxyServer.URL, "http://")
	f := NewFetcher()
	f.ProxyDecrypt = func(ciphertext string) (string, error) {
		if ciphertext == "encrypted-endpoint" {
			return endpoint, nil
		}
		return "", errors.New("unknown ciphertext")
	}
	account := domain.Account{Proxy: &domain.AccountProxyConfig{Enabled: true, EndpointCiphertext: "encrypted-endpoint"}}
	settings, err := f.accountSettings(account, f.settings())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := settings.dialContext(ctx, target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	select {
	case auth := <-connected:
		if auth != wantAuth {
			t.Fatal("account proxy authorization mismatch")
		}
	case <-ctx.Done():
		t.Fatal("account proxy was not called")
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	var response [4]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil || string(response[:]) != "ping" {
		t.Fatalf("proxy tunnel result = %q, %v", string(response[:]), err)
	}
}

func TestAccountSettingsProxyFailureDoesNotFallBack(t *testing.T) {
	f := NewFetcher()
	f.ProxyDecrypt = func(string) (string, error) { return "http://127.0.0.1:1", nil }
	account := domain.Account{Proxy: &domain.AccountProxyConfig{Enabled: true, EndpointCiphertext: "encrypted"}}
	settings, err := f.accountSettings(account, f.settings())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = settings.dialContext(ctx, "127.0.0.1:443")
	if err == nil {
		t.Fatal("failed account proxy unexpectedly connected")
	}
	if strings.Contains(err.Error(), "encrypted") {
		t.Fatal("proxy error exposed stored ciphertext")
	}
	f.ProxyDecrypt = nil
	if _, err := f.accountSettings(account, f.settings()); err == nil {
		t.Fatal("missing decryptor allowed enabled proxy")
	}
	account.Proxy.Enabled = false
	if settings, err := f.accountSettings(account, f.settings()); err != nil || settings.proxy != nil {
		t.Fatal(fmt.Sprintf("disabled proxy unexpectedly active: %v", err))
	}
}
