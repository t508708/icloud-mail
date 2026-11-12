package apple

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccountCreationFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, category, evidence, code string
		status                               int
		kind                                 error
		rate                                 bool
	}{
		{"capacity", `{"code":123,"errorMessage":"You have reached the maximum number of email addresses."}`, "capacity", "address_capacity_message", "123", 412, ErrAliasCapacity, false},
		{"address limit without period remains unknown", `{"serviceErrors":[{"message":"You have reached your limit of addresses"}]}`, "creation_not_allowed", "address_limit_unspecified", "", 412, ErrCreationNotAllowed, false},
		{"rate reason", `{"code":123,"ineligibilityReason":"rate_limit_exceeded"}`, "rate_limited", "rate_limit_reason", "123", 412, ErrRateLimited, true},
		{"rate message", `{"serviceErrors":[{"code":"456","message":"Too many requests"}]}`, "rate_limited", "request_frequency_message", "456", 412, ErrRateLimited, true},
		{"rate symbolic code", `{"errorCode":"RATE_LIMIT_EXCEEDED"}`, "rate_limited", "rate_limit_service_code", "RATE_LIMIT_EXCEEDED", 412, ErrRateLimited, true},
		{"rate secondary code", `{"code":123,"serviceErrors":[{"code":"-41015"}]}`, "rate_limited", "rate_limit_service_code", "123", 412, ErrRateLimited, true},
		{"http rate", `{}`, "rate_limited", "http_429", "", 429, ErrRateLimited, true},
		{"real account action", `{"serviceErrors":[{"code":"TERMS_REQUIRED"}]}`, "account_action", "explicit_account_action", "TERMS_REQUIRED", 412, ErrTermsRequired, false},
		{"explicit period overrides address count text", `{"ineligibilityReason":"rate_limit_exceeded","errorMessage":"Maximum number of email addresses reached"}`, "rate_limited", "rate_limit_reason", "", 412, ErrRateLimited, true},
		{"account action reason", `{"ineligibilityReason":"account_action_required"}`, "account_action", "explicit_account_action", "", 412, ErrTermsRequired, false},
		{"unknown", `{}`, "creation_not_allowed", "unclassified_precondition", "", 412, ErrCreationNotAllowed, false},
		{"unknown reason", `{"ineligibilityReason":"other"}`, "creation_not_allowed", "unclassified_precondition", "", 412, ErrCreationNotAllowed, false},
		{"maximum requests is not capacity", `{"errorMessage":"Maximum number of requests per period"}`, "creation_not_allowed", "unclassified_precondition", "", 412, ErrCreationNotAllowed, false},
		{"hourly address limit is frequency", `{"errorMessage":"Maximum number of email addresses per hour reached"}`, "rate_limited", "request_frequency_message", "", 412, ErrRateLimited, true},
		{"too many without context is unknown", `{"errorMessage":"Too many"}`, "creation_not_allowed", "unclassified_precondition", "", 412, ErrCreationNotAllowed, false},
		{"bad response", `broken`, "creation_not_allowed", "unclassified_precondition", "", 412, ErrCreationNotAllowed, false},
		{"invalid envelope cannot establish capacity", `{"code":123,"errorMessage":"Maximum number of email addresses reached","serviceErrors":"invalid"}`, "creation_not_allowed", "unclassified_precondition", "", 412, ErrCreationNotAllowed, false},
		{"server failure cannot establish rejection", `{"errorMessage":"Maximum number of email addresses reached"}`, "upstream_error", "nondefinitive_http_response", "", 503, ErrService, false},
		{"session wins", `{"errorMessage":"Too many requests"}`, "session", "http_authentication", "", 401, ErrInvalidSession, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := accountClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				r := accountResp(tc.status, tc.body)
				r.Header.Set("Retry-After", "75")
				return r, nil
			})
			s := AccountSession{SCNT: "s", APIKey: "k", ExpiresAt: time.Now().Add(time.Hour)}
			_, err := c.accountManagementCall(context.Background(), &s, http.MethodPost, "/account/manage/email/private/add", map[string]any{})
			var upstream *Error
			if !errors.As(err, &upstream) || !errors.Is(err, tc.kind) || IsRateLimited(err) != tc.rate || calls != 1 {
				t.Fatalf("err=%v kind=%v rate=%v calls=%d", err, tc.kind, IsRateLimited(err), calls)
			}
			if upstream.ServiceCode != tc.code || upstream.RetryAfter != 75*time.Second || upstream.AccountFailure == nil || upstream.AccountFailure.Category != tc.category || upstream.AccountFailure.Evidence != tc.evidence {
				t.Fatalf("diagnostics=%+v code=%q delay=%v", upstream.AccountFailure, upstream.ServiceCode, upstream.RetryAfter)
			}
			if tc.kind != ErrTermsRequired && errors.Is(err, ErrTermsRequired) {
				t.Fatal("non-terms failure became an account action")
			}
		})
	}
}

func TestAccountCreationDiagnosticsExcludeUntrustedResponseText(t *testing.T) {
	const private = "private-account@example.invalid-secret"
	r := responseData{status: 412, body: []byte(`{"code":"` + private + `","errorMessage":"` + private + `","ineligibilityReason":"` + private + `","serviceErrors":[{"message":"` + private + `"}]}`)}
	e := accountResponseError(r, ErrTermsRequired)
	classifyAccountCreationFailure(r, e)
	encoded, err := json.Marshal(e.AccountFailure)
	if err != nil || strings.Contains(string(encoded), private) || strings.Contains(e.Error(), private) {
		t.Fatalf("response data leaked: %s err=%v", encoded, err)
	}
	d := e.AccountFailure
	if !d.ReasonPresent || !d.MessagePresent || !d.ServiceCodePresent || len(d.ServiceCodeFingerprint) != 16 || d.ServiceErrorCount != 1 {
		t.Fatalf("lost presence diagnostics: %+v", d)
	}
}

func TestAccountCreationCompleteRetainsOnlyUncertainCandidate(t *testing.T) {
	for _, tc := range []struct {
		body string
		keep bool
	}{
		{`{"errorMessage":"Maximum number of email addresses reached"}`, false},
		{`{"ineligibilityReason":"rate_limit_exceeded"}`, false},
		{`{"ineligibilityReason":"other"}`, true},
	} {
		c := accountClient(t, func(*http.Request) (*http.Response, error) { return accountResp(412, tc.body), nil })
		a, _, err := c.CompleteAccountAlias(context.Background(), AccountSession{SCNT: "s", APIKey: "k"}, "candidate@icloud.com", "", "")
		if err == nil || (a.HME != "") != tc.keep {
			t.Fatalf("alias=%+v keep=%v err=%v", a, tc.keep, err)
		}
	}
}
