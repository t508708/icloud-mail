package apple

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// AccountFailureDiagnostics contains fixed categories and presence flags only.
// Upstream messages, arbitrary reason strings and credentials must stay private.
type AccountFailureDiagnostics struct {
	Category               string `json:"category"`
	Evidence               string `json:"evidence"`
	ReasonPresent          bool   `json:"reason_present"`
	ReasonFingerprint      string `json:"reason_fingerprint,omitempty"`
	MessagePresent         bool   `json:"message_present"`
	ServiceCodePresent     bool   `json:"service_code_present"`
	ServiceCodeFingerprint string `json:"service_code_fingerprint,omitempty"`
	ServiceErrorCount      int    `json:"service_error_count"`
}

type accountCreationErrorEnvelope struct {
	ErrorCode           json.RawMessage `json:"errorCode"`
	Code                json.RawMessage `json:"code"`
	ErrorMessage        string          `json:"errorMessage"`
	IneligibilityReason string          `json:"ineligibilityReason"`
	ServiceErrors       []struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"serviceErrors"`
}

func (d *AccountFailureDiagnostics) LogValue() slog.Value {
	if d == nil {
		return slog.Value{}
	}
	return slog.GroupValue(
		slog.String("category", d.Category),
		slog.String("evidence", d.Evidence),
		slog.Bool("reason_present", d.ReasonPresent),
		slog.String("reason_fingerprint", d.ReasonFingerprint),
		slog.Bool("message_present", d.MessagePresent),
		slog.Bool("service_code_present", d.ServiceCodePresent),
		slog.String("service_code_fingerprint", d.ServiceCodeFingerprint),
		slog.Int("service_error_count", d.ServiceErrorCount),
	)
}

// Only alias creation uses this classifier. Login repair and directory handling
// retain their own protocol semantics; HTTP 412 alone is not proof of terms.
func classifyAccountCreationFailure(r responseData, e *Error) {
	d := &AccountFailureDiagnostics{Category: "upstream_error", Evidence: "unclassified_response"}
	e.AccountFailure = d
	e.ServiceCode = safeAccountServiceCode(e.ServiceCode)
	var body accountCreationErrorEnvelope
	if json.Unmarshal(r.body, &body) != nil {
		body = accountCreationErrorEnvelope{}
	}
	reason := strings.ToLower(strings.TrimSpace(body.IneligibilityReason))
	d.ReasonPresent = reason != ""
	if d.ReasonPresent {
		digest := sha256.Sum256([]byte(reason))
		d.ReasonFingerprint = hex.EncodeToString(digest[:8])
	}
	d.ServiceErrorCount = len(body.ServiceErrors)
	messages := []string{strings.ToLower(body.ErrorMessage)}
	codes := []json.RawMessage{body.ErrorCode, body.Code}
	for _, item := range body.ServiceErrors {
		messages = append(messages, strings.ToLower(item.Message))
		codes = append(codes, item.Code)
	}
	termsCode, rateCode := false, false
	for _, raw := range codes {
		value := strings.TrimSpace(string(raw))
		if value == "" || value == "null" || value == "0" || value == `"0"` {
			continue
		}
		var decoded string
		if json.Unmarshal(raw, &decoded) == nil {
			value = decoded
		}
		if !d.ServiceCodePresent {
			digest := sha256.Sum256([]byte(value))
			d.ServiceCodeFingerprint = hex.EncodeToString(digest[:8])
			d.ServiceCodePresent = true
			e.ServiceCode = safeAccountServiceCode(value)
		}
		switch strings.ToUpper(strings.TrimSpace(value)) {
		case "TERMS_REQUIRED", "ACCOUNT_ACTION_REQUIRED":
			termsCode = true
		case "RATE_LIMIT_EXCEEDED", "TOO_MANY_REQUESTS", hmeRateLimitCodeBatch:
			rateCode = true
		}
	}
	capacityMessage, rateMessage, ambiguousLimit := false, false, false
	for _, message := range messages {
		d.MessagePresent = d.MessagePresent || strings.TrimSpace(message) != ""
		for _, phrase := range []string{"maximum number of addresses", "maximum number of email addresses", "maximum number of private email addresses", "maximum number of hide my email addresses"} {
			capacityMessage = capacityMessage || strings.Contains(message, phrase)
		}
		ambiguousLimit = ambiguousLimit || strings.Contains(message, "limit of addresses")
		rateMessage = rateMessage || strings.Contains(message, "too many requests") || strings.Contains(message, "too many attempts")
		if capacityMessage && (strings.Contains(message, "per hour") || strings.Contains(message, "per day") || strings.Contains(message, "in the last hour") || strings.Contains(message, "within 24 hours")) {
			rateMessage = true
		}
	}
	switch {
	case e.Kind == ErrInvalidSession:
		d.Category, d.Evidence = "session", "http_authentication"
	case r.status == http.StatusTooManyRequests:
		e.Kind = ErrRateLimited
		d.Category, d.Evidence = "rate_limited", "http_429"
	case r.status == 0 || r.status >= http.StatusInternalServerError:
		d.Evidence = "nondefinitive_http_response"
	case reason == "rate_limit_exceeded" || rateCode || rateMessage:
		e.Kind = ErrRateLimited
		d.Category = "rate_limited"
		switch {
		case reason == "rate_limit_exceeded":
			d.Evidence = "rate_limit_reason"
		case rateCode:
			d.Evidence = "rate_limit_service_code"
		default:
			d.Evidence = "request_frequency_message"
		}
	case capacityMessage:
		e.Kind = ErrAliasCapacity
		d.Category, d.Evidence = "capacity", "address_capacity_message"
	case termsCode || reason == "terms_required" || reason == "account_action_required":
		e.Kind = ErrTermsRequired
		d.Category, d.Evidence = "account_action", "explicit_account_action"
	case r.status == http.StatusPreconditionFailed:
		if _, repair := loginRepairLocation(r, accountWidget); repair {
			e.Kind = ErrTermsRequired
			d.Category, d.Evidence = "account_action", "account_repair_challenge"
		} else {
			e.Kind = ErrCreationNotAllowed
			d.Category, d.Evidence = "creation_not_allowed", "unclassified_precondition"
			if ambiguousLimit {
				d.Evidence = "address_limit_unspecified"
			}
		}
	}
}

// Unknown symbolic codes are represented by a digest, never copied to logs.
func safeAccountServiceCode(value string) string {
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return value
	}
	switch value {
	case "TERMS_REQUIRED", "ACCOUNT_ACTION_REQUIRED", "RATE_LIMIT_EXCEEDED", "TOO_MANY_REQUESTS":
		return value
	default:
		return ""
	}
}
