package httpserver

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"icloud-api/internal/apple"
	"icloud-api/internal/hmesync"
)

func TestAppleCreationFailuresHaveDistinctPublicMessages(t *testing.T) {
	for _, tc := range []struct {
		kind           error
		code, contains string
		status         int
	}{
		{hmesync.ErrAliasCapacity, hmesync.CodeAliasCapacity, "数量达到上限", http.StatusConflict},
		{hmesync.ErrCreationNotAllowed, hmesync.CodeCreationNotAllowed, "未明确说明原因", http.StatusConflict},
		{hmesync.ErrRateLimited, hmesync.CodeRateLimited, "请求过于频繁", http.StatusTooManyRequests},
		{hmesync.ErrAccountActionRequired, hmesync.CodeAccountActionRequired, "请前往 Apple 官网", http.StatusConflict},
	} {
		t.Run(tc.code, func(t *testing.T) {
			diagnostics := &apple.AccountFailureDiagnostics{Category: "creation_not_allowed", Evidence: "unclassified_precondition"}
			upstream := &apple.Error{StatusCode: 412, AccountFailure: diagnostics}
			result := classifyManualAliasError(errors.Join(tc.kind, upstream))
			if result.Code != tc.code || result.Status != tc.status || !strings.Contains(result.Message, tc.contains) || result.AccountFailureDiagnostics != diagnostics {
				t.Fatalf("result=%+v", result)
			}
			if tc.kind != hmesync.ErrAccountActionRequired && strings.Contains(result.Message, "请前往 Apple 官网") {
				t.Fatal("unrelated failure requests an account action")
			}
		})
	}
}
