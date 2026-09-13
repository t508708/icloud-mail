package apple

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// Apple's Hide My Email service reports the batch creation throttle inside
	// an HTTP 200 response. It is not expressed as HTTP 429.
	hmeRateLimitCodeBatch = "-41015"
)

var (
	ErrInvalidConfig    = errors.New("invalid Apple client configuration")
	ErrInvalidSession   = errors.New("invalid or expired Apple session")
	ErrHMEUnavailable   = errors.New("Hide My Email service is not available for this session")
	ErrAuthentication   = errors.New("Apple authentication failed")
	ErrTwoFactorCode    = errors.New("invalid Apple two-factor code")
	ErrTermsRequired    = errors.New("Apple account action required")
	ErrResponseTooLarge = errors.New("Apple response exceeds configured limit")
	ErrInvalidResponse  = errors.New("invalid Apple response")
	ErrService          = errors.New("Apple service returned an error")
)

// Error is the typed error returned for transport, HTTP, protocol and service
// failures. Response bodies are deliberately excluded because they can carry
// authentication material.
type Error struct {
	Op          string
	Kind        error
	StatusCode  int
	ServiceCode string
	Retryable   bool
	// RetryAfter is a non-sensitive server delay hint, not permission to replay
	// the operation. Zero means no positive delay is available.
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := "Apple request failed"
	if e.Op != "" {
		message = "Apple " + e.Op + " failed"
	}
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.ServiceCode != "" {
		message += " (service " + e.ServiceCode + ")"
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err != nil {
		return e.Err
	}
	return e.Kind
}

func (e *Error) Is(target error) bool {
	// Keep this comparison shallow. The errors package follows Unwrap after
	// calling Is, so walking Err here would duplicate traversal of the chain.
	return e != nil && target == e.Kind
}

func operationError(op string, kind error, status int, cause error) *Error {
	return &Error{
		Op:         op,
		Kind:       kind,
		StatusCode: status,
		Retryable:  retryableStatus(status) || (status == 0 && kind == ErrService && cause != nil),
		Err:        cause,
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// RetryDelay returns the largest non-negative RetryAfter hint in err, including
// wrapped and joined errors. It returns zero when no positive hint is present.
// A delay does not change Retryable or make an operation safe to replay.
func RetryDelay(err error) time.Duration {
	var delay time.Duration
	if upstream, ok := err.(*Error); ok && upstream != nil {
		delay = max(delay, upstream.RetryAfter)
	}
	switch unwrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range unwrapped.Unwrap() {
			delay = max(delay, RetryDelay(child))
		}
	case interface{ Unwrap() error }:
		delay = max(delay, RetryDelay(unwrapped.Unwrap()))
	}
	return delay
}

// parseRetryAfter accepts RFC 9110 delay-seconds and HTTP-date values. Valid
// waits beyond time.Duration's range saturate instead of wrapping or being
// capped to a shorter, operationally convenient retry interval.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.Trim(value, " \t")
	if value == "" {
		return 0
	}
	if value[0] >= '0' && value[0] <= '9' {
		// Validate the entire value before considering overflow: a huge number
		// followed by non-digits is still invalid, not a saturated wait.
		for i := range len(value) {
			if value[i] < '0' || value[i] > '9' {
				return 0
			}
		}
		const maxDuration = time.Duration(1<<63 - 1)
		seconds, err := strconv.ParseUint(value, 10, 64)
		if errors.Is(err, strconv.ErrRange) || seconds > uint64(maxDuration/time.Second) {
			return maxDuration
		}
		if err != nil {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	if strings.Contains(value, "-") {
		// RFC 850's two-digit year uses a rolling 50-year window, not the
		// fixed century cutoff used by time.Parse.
		latest := now.AddDate(50, 0, 0)
		year := latest.Year()/100*100 + date.Year()%100
		date = date.AddDate(year-date.Year(), 0, 0)
		if date.After(latest) {
			date = date.AddDate(-100, 0, 0)
		}
	}
	// Time.Sub itself saturates at the duration limits for distant dates.
	return max(time.Duration(0), date.Sub(now))
}

// IsRateLimited reports both HTTP-level throttling and Hide My Email business
// throttles returned in a successful HTTP response envelope.
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if upstream, ok := err.(*Error); ok && isRateLimitedError(upstream) {
		return true
	}
	switch unwrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range unwrapped.Unwrap() {
			if IsRateLimited(child) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return IsRateLimited(unwrapped.Unwrap())
	}
	return false
}

func isRateLimitedError(upstream *Error) bool {
	if upstream == nil {
		return false
	}
	if upstream.StatusCode == http.StatusTooManyRequests {
		return true
	}
	switch strings.TrimSpace(upstream.ServiceCode) {
	case hmeRateLimitCodeBatch:
		return true
	default:
		return false
	}
}
