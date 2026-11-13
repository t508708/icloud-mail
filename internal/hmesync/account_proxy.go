package hmesync

import (
	"context"
	"errors"
	"fmt"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/netproxy"
)

type accountProxyCipher interface {
	Decrypt(string) (string, error)
}

func cloneAccountProxy(proxy *domain.AccountProxyConfig) *domain.AccountProxyConfig {
	if proxy == nil {
		return nil
	}
	copy := *proxy
	return &copy
}

func sameAccountProxy(left, right *domain.AccountProxyConfig) bool {
	if left == nil {
		left = &domain.AccountProxyConfig{}
	}
	if right == nil {
		right = &domain.AccountProxyConfig{}
	}
	return left.Enabled == right.Enabled && left.Revision == right.Revision &&
		left.EndpointCiphertext == right.EndpointCiphertext &&
		left.UpstreamCiphertext == right.UpstreamCiphertext
}

func (s *Service) accountProxyForAccount(ctx context.Context, account domain.Account) (context.Context, error) {
	if account.Proxy == nil || !account.Proxy.Enabled {
		return ctx, nil
	}
	cipher, ok := s.cipher.(accountProxyCipher)
	if !ok {
		return nil, errors.New("Apple proxy cipher is unavailable")
	}
	endpoint, err := cipher.Decrypt(account.Proxy.EndpointCiphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt Apple proxy endpoint: %w", err)
	}
	upstream := ""
	if account.Proxy.UpstreamCiphertext != "" {
		upstream, err = cipher.Decrypt(account.Proxy.UpstreamCiphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt Apple upstream proxy: %w", err)
		}
	}
	config := netproxy.Config{Endpoint: endpoint, Upstream: upstream}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid Apple proxy configuration")
	}
	return apple.WithAccountProxy(ctx, account.ID, config), nil
}
