package httpserver

import (
	"errors"
	"strings"
	"testing"

	"icloud-api/internal/apple"
	"icloud-api/internal/hmesync"
)

func TestAppleSessionDiagnosticsKeepUpstreamStageWithoutResponseData(t *testing.T) {
	upstream := &apple.Error{Op: "validate Apple session", Kind: apple.ErrInvalidSession, StatusCode: 421, ServiceCode: "FIXTURE_CODE"}
	err := errors.Join(hmesync.ErrSessionExpired, upstream)
	result := classifyAdminAPIAppleError(err)
	if result.Code != hmesync.CodeSessionExpired || result.UpstreamOperation != upstream.Op || result.UpstreamStatus != 421 || result.UpstreamServiceCode != "FIXTURE_CODE" {
		t.Fatalf("lost session diagnostics: %#v", result)
	}
	if strings.Contains(result.Message, "FIXTURE_CODE") {
		t.Fatal("raw diagnostic leaked into user message")
	}
}
