package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/domain"
	"icloud-api/internal/netproxy"
	"icloud-api/internal/store"
)

type adminAPIProxyInput struct {
	Enabled       bool   `json:"enabled"`
	Endpoint      string `json:"endpoint"`
	Upstream      string `json:"upstream"`
	ClearUpstream bool   `json:"clear_upstream"`
}

type adminAPIProxyDTO struct {
	Enabled            bool `json:"enabled"`
	Configured         bool `json:"configured"`
	UpstreamConfigured bool `json:"upstream_configured"`
}

type adminAPIProxyTestRequest struct {
	AccountID int64               `json:"account_id"`
	Proxy     *adminAPIProxyInput `json:"proxy"`
}

func adminAPIAccountProxyFromDomain(account domain.Account) adminAPIProxyDTO {
	if account.Proxy == nil {
		return adminAPIProxyDTO{}
	}
	return adminAPIProxyDTO{
		Enabled:            account.Proxy.Enabled,
		Configured:         account.Proxy.EndpointCiphertext != "",
		UpstreamConfigured: account.Proxy.UpstreamCiphertext != "",
	}
}

func (s *Server) prepareAccountProxy(input *adminAPIProxyInput, previous domain.Account) (*domain.AccountProxyConfig, string, error) {
	if input == nil {
		return nil, "", nil
	}
	config := domain.AccountProxyConfig{}
	if previous.Proxy != nil {
		config = *previous.Proxy
	}
	config.Enabled = input.Enabled
	if input.ClearUpstream && strings.TrimSpace(input.Upstream) != "" {
		return nil, "清除前置代理时请留空前置代理输入", nil
	}
	if endpoint := strings.TrimSpace(input.Endpoint); endpoint != "" {
		if _, err := netproxy.Parse(endpoint); err != nil {
			return nil, "落地代理地址或端口格式有误", nil
		}
		ciphertext, err := s.cipher.Encrypt(endpoint)
		if err != nil {
			return nil, "", err
		}
		config.EndpointCiphertext = ciphertext
	}
	if input.ClearUpstream {
		config.UpstreamCiphertext = ""
	} else if upstream := strings.TrimSpace(input.Upstream); upstream != "" {
		if _, err := netproxy.Parse(upstream); err != nil {
			return nil, "前置代理地址或端口格式有误", nil
		}
		ciphertext, err := s.cipher.Encrypt(upstream)
		if err != nil {
			return nil, "", err
		}
		config.UpstreamCiphertext = ciphertext
	}
	if config.Enabled {
		if config.EndpointCiphertext == "" {
			return nil, "开启代理后请填写落地代理地址", nil
		}
		if _, err := s.cipher.Decrypt(config.EndpointCiphertext); err != nil {
			return nil, "", err
		}
	}
	return &config, "", nil
}

func (s *Server) adminAPITestAccountProxy(c *gin.Context) {
	var input adminAPIProxyTestRequest
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	if input.AccountID < 0 {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "主号 ID 无效")
		return
	}
	var existing domain.Account
	if input.AccountID > 0 {
		var err error
		existing, err = s.store.GetAccount(c.Request.Context(), input.AccountID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeAdminAPIError(c, http.StatusNotFound, "NOT_FOUND", "主号不存在")
			} else {
				s.writeAdminAPIInternalError(c, err)
			}
			return
		}
	}
	config, message, err := s.prepareAccountProxy(input.Proxy, existing)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	if message != "" {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", message)
		return
	}
	if config == nil {
		config = existing.Proxy
	}
	if config == nil || !config.Enabled || config.EndpointCiphertext == "" {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "请先开启并填写落地代理")
		return
	}
	endpoint, err := s.cipher.Decrypt(config.EndpointCiphertext)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	upstream := ""
	if config.UpstreamCiphertext != "" {
		upstream, err = s.cipher.Decrypt(config.UpstreamCiphertext)
		if err != nil {
			s.writeAdminAPIInternalError(c, err)
			return
		}
	}
	proxy := netproxy.Config{Endpoint: endpoint, Upstream: upstream}
	transport, err := proxy.HTTPTransport()
	if err != nil {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "代理地址格式有误")
		return
	}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.icloud.com/", nil)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		message, hop, stage := describeProxyTestFailure(err)
		s.logger.Warn("主号代理连通检查失败", "hop", hop, "stage", stage, "request_id", requestID(c))
		writeAdminAPIError(c, http.StatusBadGateway, "PROXY_CONNECT_FAILED", message)
		return
	}
	defer response.Body.Close()
	writeAdminAPIData(c, http.StatusOK, gin.H{"ok": true, "duration_ms": time.Since(start).Milliseconds(), "status_code": response.StatusCode})
}

func describeProxyTestFailure(err error) (message, hop, stage string) {
	var connectErr *netproxy.ConnectError
	if !errors.As(err, &connectErr) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "访问 iCloud 超时，请稍后重试", "target", "timeout"
		}
		return "代理已连接，但访问 iCloud 失败，请检查代理的目标访问规则", "target", "request"
	}
	name := "落地代理"
	if connectErr.Hop == "upstream" {
		name = "前置代理"
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return name + "连接超时，请检查网络及代理服务状态", connectErr.Hop, "timeout"
	}
	switch connectErr.Stage {
	case "connect":
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return name + "域名解析失败，请检查代理主机名", connectErr.Hop, "dns"
		}
		return name + "连接失败，请检查主机地址、端口及服务器出口网络", connectErr.Hop, "connect"
	case "tls":
		return name + " TLS 握手失败，请确认 https 协议和代理证书", connectErr.Hop, "tls"
	case "authentication":
		return name + "认证失败，请检查用户名、密码及代理协议", connectErr.Hop, "authentication"
	default:
		return name + "隧道建立失败，请检查代理协议及目标访问权限", connectErr.Hop, "tunnel"
	}
}
