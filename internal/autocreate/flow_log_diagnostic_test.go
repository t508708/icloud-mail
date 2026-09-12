package autocreate

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"icloud-api/internal/apple"
)

type pendingValidateError struct{ err error }

func (e pendingValidateError) Error() string             { return e.err.Error() }
func (e pendingValidateError) Unwrap() error             { return e.err }
func (e pendingValidateError) PendingConfirmation() bool { return true }

type remoteValidateError struct{ err error }

func (e remoteValidateError) Error() string                  { return e.err.Error() }
func (e remoteValidateError) Unwrap() error                  { return e.err }
func (e remoteValidateError) RemoteSideEffectPossible() bool { return true }

func TestDiagnoseAppleSessionTransportRetryReason(t *testing.T) {
	const sensitive = "raw-service-code-cookie"
	err := &apple.Error{Op: "validate Apple session", Kind: apple.ErrService, Retryable: true, Err: errors.New(sensitive)}
	info := diagnoseAliasCreationError(err)
	if info.code != "APPLE_UPSTREAM_ERROR" || !strings.Contains(info.reason, "无需重新登录") || !strings.Contains(info.reason, "下一次计划自动重试") {
		t.Fatalf("transport reason = %#v", info)
	}
	if strings.Contains(info.reason, sensitive) || strings.Contains(info.reason, "service") {
		t.Fatalf("sensitive detail leaked: %q", info.reason)
	}
}

func TestDiagnoseAppleSessionHTTP500ReasonAndPreservesSemanticCodes(t *testing.T) {
	err := &apple.Error{Op: "validate Apple session", Kind: apple.ErrService, StatusCode: http.StatusBadGateway, Retryable: true, ServiceCode: "-50001"}
	info := diagnoseAliasCreationError(err)
	if !strings.Contains(info.reason, "HTTP 502") || !strings.Contains(info.reason, "下一次计划自动重试") || strings.Contains(info.reason, "-41015") {
		t.Fatalf("HTTP reason = %#v", info)
	}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"session", apple.ErrInvalidSession, "APPLE_SESSION_EXPIRED"},
		{"rate limit", &apple.Error{Op: "validate Apple session", Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, ServiceCode: "-41015"}, "APPLE_RATE_LIMITED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagnoseAliasCreationError(tc.err).code; got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestDiagnoseAppleSessionPreservesPendingAndRemoteReasons(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(error) error
	}{
		{"pending", func(err error) error { return pendingValidateError{err} }},
		{"remote", func(err error) error { return remoteValidateError{err} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &apple.Error{Op: "validate Apple session", Kind: apple.ErrService, Retryable: true}
			info := diagnoseAliasCreationError(tc.wrap(upstream))
			if info.reason != aliasCreationErrorReason("APPLE_UPSTREAM_ERROR") {
				t.Fatalf("reason = %q, want generic upstream reason", info.reason)
			}
		})
	}
}
