package autologincode

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/netproxy"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Tests replace the transport, so fixtures never contact DNS, a provider, or Apple.
func fixtureClient(status int, body string) *Client {
	c := NewClient()
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			return nil, errors.New("unexpected request")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return c
}

func TestCodeProviderUsesAccountProxy(t *testing.T) {
	seen := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy request method = %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("HTTP proxy does not support CONNECT tunneling")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		if err := rw.Flush(); err != nil {
			t.Error(err)
			return
		}
		request, err := http.ReadRequest(rw.Reader)
		if err != nil {
			t.Error(err)
			return
		}
		seen <- r.Host + " " + request.Host + request.URL.Path
		body := `{"otp":"123456"}`
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 16\r\n\r\n" + body)
		_ = rw.Flush()
	}))
	defer proxy.Close()
	ctx := WithProxy(context.Background(), netproxy.Config{Endpoint: proxy.URL})
	result, err := NewClient().Fetch(ctx, "http://8.8.8.8/code")
	if err != nil || result.Code != "123456" {
		t.Fatalf("proxied provider result = %#v, %v", result, err)
	}
	select {
	case target := <-seen:
		if target != "8.8.8.8:80 8.8.8.8/code" {
			t.Fatalf("proxy target = %s", target)
		}
	default:
		t.Fatal("provider request did not cross the proxy")
	}
}

func TestProviderResponseFixtures(t *testing.T) {
	tests := []struct {
		name, body, code string
		wantErr          error
	}{
		{"observed_provider_two_plain_text", "AppleID 登录验证码:123456", "123456", nil},
		{"plain_code", " 123456\n", "123456", nil},
		{"explicit_otp", `{"otp":"123456"}`, "123456", nil},
		{"explicit_verification_code", `{"verificationCode":123456}`, "123456", nil},
		{"nested_generic_code", `{"code":200,"data":{"code":"123456"}}`, "123456", nil},
		{"nested_result", `{"success":true,"result":{"data":{"verification_code":"123456"}}}`, "123456", nil},
		{"sms_text", `{"code":200,"msg":"AppleID 登录验证码:123456"}`, "123456", nil},
		{"plain_english", "Your Apple ID code: 123456", "123456", nil},
		{"status_code_only", `{"code":123456,"id":654321}`, "", ErrNoCode},
		{"unrelated_id", `{"id":"123456","message":"account 234567"}`, "", ErrNoCode},
		{"false_status", `{"status":false,"data":{"otp":"123456"}}`, "", ErrNoCode},
		{"false_success", `{"success":false,"code":"123456"}`, "", ErrNoCode},
		{"error_digits", `{"error":{"code":123456},"message":"error 654321"}`, "", ErrNoCode},
		{"contextual_error_digits", `{"message":"验证码请求失败，错误编号123456"}`, "", ErrNoCode},
		{"waiting", `{"code":200,"data":null,"msg":"等待验证码"}`, "", ErrNoCode},
		{"empty", "", "", ErrNoCode},
		{"two_codes", `{"otp":"123456","data":{"verificationCode":"234567"}}`, "", ErrInvalidResponse},
		{"two_in_text", "验证码: 123456 或者 234567", "", ErrInvalidResponse},
		{"same_code_duplicate", `{"otp":"123456","data":{"verificationCode":"123456"}}`, "123456", nil},
		{"longer_number", "验证码: 1234567", "", ErrNoCode},
		{"malformed", `{"otp":"123456"`, "", ErrInvalidResponse},
		{"trailing_json", `{"otp":"123456"} {}`, "", ErrInvalidResponse},
		{"array", `[{"otp":"123456"}]`, "", ErrInvalidResponse},
		{"html", `<html>验证码<script>var account=123456</script></html>`, "", ErrInvalidResponse},
		{"html_in_message", `{"message":"验证码<script>123456</script>"}`, "", ErrNoCode},
		{"oversized", strings.Repeat("1", maxResponseBytes+1), "", ErrInvalidResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := fixtureClient(200, tt.body).Fetch(context.Background(), "http://provider.example:8818/api/getCode?ID=secret")
			if !errors.Is(err, tt.wantErr) || result.Code != tt.code {
				t.Fatalf("unexpected result: code match=%t, err=%v", result.Code == tt.code, err)
			}
			if err == nil && len(result.Fingerprint) != 64 {
				t.Fatal("missing stable fingerprint")
			}
		})
	}
}

func TestFingerprintAndTimestamp(t *testing.T) {
	first, err := fixtureClient(200, `{"request_id":"one","timestamp":1800000000,"data":{"otp":"123456","receivedAt":"2026-10-22T12:00:00+08:00"}}`).Fetch(context.Background(), "https://provider.example/code")
	if err != nil || first.ReceivedAt == nil {
		t.Fatal("expected received timestamp")
	}
	second, err := fixtureClient(200, `{"request_id":"two","timestamp":1800000001,"data":{"otp":"123456","receivedAt":"2026-10-22T04:00:00Z"}}`).Fetch(context.Background(), "https://provider.example/code")
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatal("dynamic provider fields changed fingerprint")
	}
	third, err := fixtureClient(200, `{"data":{"otp":"123456","receivedAt":"2026-10-22T04:01:00Z"}}`).Fetch(context.Background(), "https://provider.example/code")
	if err != nil || first.Fingerprint == third.Fingerprint {
		t.Fatal("new message timestamp did not change fingerprint")
	}
}

func TestResponseStatusAndSecretErrors(t *testing.T) {
	for _, status := range []int{301, 302, 401, 403, 429, 500, 503} {
		_, err := fixtureClient(status, "SECRET_BODY 123456").Fetch(context.Background(), "https://provider.example/?token=SECRET_URL")
		if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "123456") {
			t.Fatalf("unexpected error for status %d", status)
		}
	}
	_, err := fixtureClient(204, "").Fetch(context.Background(), "https://provider.example/code")
	if !errors.Is(err, ErrNoCode) {
		t.Fatal("204 should be pending")
	}
	c := NewClient()
	c.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("SECRET_URL SECRET_BODY") })
	_, err = c.Fetch(context.Background(), "https://provider.example/?secret=SECRET_URL")
	if err != ErrUnavailable {
		t.Fatal("transport error was not sanitized")
	}
	if c.httpClient.CheckRedirect(nil, nil) != http.ErrUseLastResponse || c.httpClient.Jar != nil || c.httpClient.Timeout != 10*time.Second {
		t.Fatal("unsafe client defaults")
	}
}

func TestValidateURL(t *testing.T) {
	for _, value := range []string{
		"http://devices.orangeid.top:56133/api/free/v4/getcode?id=EXAMPLE",
		"http://43.138.208.78:8818/api/Accounts/getCode?ID=EXAMPLE",
		"https://provider.example/code",
		"https://[2606:4700:4700::1111]:443/code",
	} {
		if ValidateURL(value) != nil {
			t.Fatal("expected valid public URL")
		}
	}
	for _, value := range []string{
		"", "file:///etc/passwd", "https://user:password@provider.example/code", "https://provider.example/#secret",
		"http://127.0.0.1/code", "http://10.1.1.1/code", "http://172.16.0.1/code", "http://192.168.0.1/code", "http://169.254.169.254/", "http://100.64.1.1/",
		"http://[::1]/", "http://[::ffff:43.138.208.78]/", "http://[fc00::1]/", "http://[fe80::1%25eth0]/", "http://[64:ff9b::7f00:1]/",
		"http://provider.example:0/", "http://provider.example:65536/", "http://provider.example:/", "http://provider..example/", "http://-provider.example/", " https://provider.example/",
		"http://provider.example\\@127.0.0.1/", "http://[2001:db8::1]/",
	} {
		if ValidateURL(value) != ErrInvalidURL {
			t.Fatal("expected invalid URL")
		}
	}
}

func TestPublicIPClassification(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "0.0.0.0", "224.0.0.1", "240.0.0.1", "192.0.2.1", "198.18.0.1", "203.0.113.1", "::ffff:8.8.8.8", "::1", "ff02::1", "fc00::1", "2001:db8::1", "2002:7f00:1::"} {
		if isPublicIP(netip.MustParseAddr(value)) {
			t.Fatalf("accepted nonpublic address %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "43.138.208.78", "2606:4700:4700::1111"} {
		if !isPublicIP(netip.MustParseAddr(value)) {
			t.Fatal("blocked public address")
		}
	}
}

func TestPrivateLiteralDialNeverConnects(t *testing.T) {
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "[::ffff:127.0.0.1]:80", "169.254.169.254:80"} {
		conn, err := publicDialContext(context.Background(), "tcp", address)
		if conn != nil || err != ErrInvalidURL {
			t.Fatal("private dial was not rejected")
		}
	}
}

func TestDNSMixedAnswersAndPinnedDial(t *testing.T) {
	for _, ips := range [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::ffff:127.0.0.1")},
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::ffff:169.254.169.254")},
	} {
		called := false
		_, err := dialPublic(context.Background(), "tcp", "provider.example:8818",
			func(context.Context, string, string) ([]netip.Addr, error) { return ips, nil },
			func(context.Context, string, string) (net.Conn, error) { called = true; return nil, ErrUnavailable },
		)
		if err != ErrInvalidURL || called {
			t.Fatal("mixed DNS answers reached dial")
		}
	}
	lookups, dials := 0, 0
	_, err := dialPublic(context.Background(), "tcp", "provider.example:8818",
		func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		func(_ context.Context, network, address string) (net.Conn, error) {
			dials++
			if network != "tcp" || address != "8.8.8.8:8818" {
				t.Fatal("dial did not pin the verified IP")
			}
			return nil, ErrUnavailable
		},
	)
	if err != ErrUnavailable || lookups != 1 || dials != 1 {
		t.Fatal("unexpected DNS/dial boundary")
	}
}

func TestResolverMappedPublicIPv4IsCanonicalizedBeforeDial(t *testing.T) {
	for _, input := range []string{"43.138.208.78", "137.131.2.92"} {
		mapped, ok := netip.AddrFromSlice(net.ParseIP(input))
		if !ok || !mapped.Is4In6() {
			t.Fatal("fixture must reproduce net.Resolver.LookupNetIP representation")
		}
		called := false
		_, err := dialPublic(context.Background(), "tcp", "provider.example:8818",
			func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{mapped}, nil
			},
			func(_ context.Context, network, address string) (net.Conn, error) {
				called = true
				if network != "tcp" || address != net.JoinHostPort(input, "8818") {
					t.Fatal("mapped public A record was not pinned to canonical IPv4")
				}
				return nil, ErrUnavailable
			},
		)
		if err != ErrUnavailable || !called {
			t.Fatal("public A record was rejected before dial")
		}
	}
}
