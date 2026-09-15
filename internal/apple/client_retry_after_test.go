package apple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientRetryAfterErrorPaths(t *testing.T) {
	operations := []struct {
		name     string
		path     string
		mutation bool
		call     func(*Client, Session) (Session, error)
	}{
		{
			name: "validate", path: "/setup/ws/1/validate",
			call: func(client *Client, session Session) (Session, error) {
				return client.Validate(context.Background(), session)
			},
		},
		{
			name: "list", path: "/v2/hme/list",
			call: func(client *Client, session Session) (Session, error) {
				_, updated, err := client.ListAliases(context.Background(), session)
				return updated, err
			},
		},
		{
			name: "deactivate", path: "/v1/hme/deactivate", mutation: true,
			call: func(client *Client, session Session) (Session, error) {
				return client.DeactivateAlias(context.Background(), session, "remote-id")
			},
		},
		{
			name: "delete", path: "/v1/hme/delete", mutation: true,
			call: func(client *Client, session Session) (Session, error) {
				return client.DeleteAlias(context.Background(), session, "remote-id")
			},
		},
	}
	tests := []struct {
		name   string
		status int
		body   string
		kind   error
	}{
		{name: "HTTP 429", status: http.StatusTooManyRequests, body: `{"errorCode":"RATE_LIMITED","message":"body-secret"}`, kind: ErrService},
		{name: "HTTP 503", status: http.StatusServiceUnavailable, body: `{}`, kind: ErrService},
		{name: "HTTP 200 batch throttle", status: http.StatusOK, body: `{"success":false,"error":{"errorCode":-41015,"message":"body-secret"}}`, kind: ErrService},
		{name: "session expired", status: http.StatusUnauthorized, body: `{}`, kind: ErrInvalidSession},
		{name: "malformed response", status: http.StatusOK, body: `{"success":`, kind: ErrInvalidResponse},
		{name: "missing success", status: http.StatusOK, body: `{"result":null}`, kind: ErrInvalidResponse},
		{name: "empty response", status: http.StatusNoContent, kind: ErrInvalidResponse},
		{name: "oversize response", status: http.StatusTooManyRequests, body: strings.Repeat("x", 1025), kind: ErrResponseTooLarge},
		{name: "body read failure", status: http.StatusTooManyRequests, kind: ErrInvalidResponse},
	}
	for _, operation := range operations {
		for _, test := range tests {
			t.Run(operation.name+"/"+test.name, func(t *testing.T) {
				requests := 0
				var failedBody *retryAfterFailingBody
				client, err := NewClient(Config{
					MaxResponseBytes: 1024,
					Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						requests++
						if request.URL.Path != operation.path {
							t.Fatalf("path = %q, want %q", request.URL.Path, operation.path)
						}
						response := testResponse(request, test.status, test.body, http.Header{
							"Retry-After":           {" \t00075\t "},
							"X-Apple-Session-Token": {"header-secret"},
						})
						if test.name == "body read failure" {
							failedBody = &retryAfterFailingBody{}
							response.Body = failedBody
						}
						return response, nil
					}),
				})
				if err != nil {
					t.Fatal(err)
				}
				session := retryAfterTestSession()
				updated, err := operation.call(client, session)
				wantKind := test.kind
				// Preserve existing envelope classification; add only the delay.
				if operation.name == "validate" && test.name == "HTTP 200 batch throttle" {
					wantKind = ErrInvalidResponse
				}
				if operation.name == "list" && test.name == "missing success" {
					wantKind = ErrService
				}
				if operation.name == "list" && test.name == "session expired" {
					wantKind = ErrHMEAuthentication
				}
				if !errors.Is(err, wantKind) {
					t.Fatalf("error = %v, want %v", err, wantKind)
				}
				var typed *Error
				wantRetryable := !operation.mutation && retryableStatus(test.status)
				if !errors.As(err, &typed) || typed.StatusCode != test.status || typed.Retryable != wantRetryable || typed.RetryAfter != 75*time.Second {
					t.Fatalf("typed error = %#v, want status %d, retryable %v, delay 75s", typed, test.status, wantRetryable)
				}
				if got := RetryDelay(fmt.Errorf("operation: %w", err)); got != 75*time.Second {
					t.Fatalf("wrapped RetryDelay = %v, want 75s", got)
				}
				if test.status == http.StatusTooManyRequests || (test.name == "HTTP 200 batch throttle" && operation.name != "validate") {
					if !IsRateLimited(err) {
						t.Fatalf("error is not rate limited: %#v", typed)
					}
				}
				if operation.name != "validate" && test.name == "HTTP 200 batch throttle" && typed.ServiceCode != hmeRateLimitCodeBatch {
					t.Fatalf("service code = %q, want %q", typed.ServiceCode, hmeRateLimitCodeBatch)
				}
				if requests != 1 {
					t.Fatalf("requests = %d, want 1 (hint must not replay)", requests)
				}
				if wantKind == ErrHMEAuthentication {
					if updated.SessionToken != session.SessionToken {
						t.Fatal("directory authentication failure overwrote saved credentials")
					}
				} else if updated.SessionToken != "header-secret" {
					t.Fatal("session header was not preserved")
				}
				if failedBody != nil && (!failedBody.closed || !errors.Is(err, io.ErrUnexpectedEOF)) {
					t.Fatal("failed body was not closed or its cause was lost")
				}
				encoded, marshalErr := json.Marshal(typed)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				for _, secret := range []string{"header-secret", "body-secret", "00075"} {
					if strings.Contains(string(encoded)+fmt.Sprintf("%+v %#v", err, typed), secret) {
						t.Fatalf("error retained sensitive content or raw Retry-After: %q", secret)
					}
				}
			})
		}
	}
}

func TestResponseErrorRetryAfterValues(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "missing"},
		{name: "invalid", value: "secret-invalid-header"},
		{name: "negative", value: "-120"},
		{name: "past", value: "Sun, 06 Nov 1994 08:49:37 GMT"},
		{name: "saturated", value: strings.Repeat("9", 100), want: time.Duration(1<<63 - 1)},
		{name: "HTTP date", value: deadline.Format(http.TimeFormat)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := time.Now()
			err := responseError("list", ErrService, responseData{
				status: http.StatusTooManyRequests,
				header: http.Header{"Retry-After": {test.value}, "Date": {"Fri, 31 Dec 9999 23:59:59 GMT"}},
				body:   []byte(`{"message":"body-secret"}`),
			})
			after := time.Now()
			got := RetryDelay(err)
			if test.name == "HTTP date" {
				if got < deadline.Sub(after) || got > deadline.Sub(before) {
					t.Fatalf("HTTP-date delay = %v, want local clock range [%v, %v]", got, deadline.Sub(after), deadline.Sub(before))
				}
			} else if got != test.want {
				t.Fatalf("RetryDelay = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidate429PreservesNinetySecondRetryAfter(t *testing.T) {
	requests := 0
	client, err := NewClient(Config{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.URL.Path != "/setup/ws/1/validate" {
			t.Fatalf("unexpected request path %q", request.URL.Path)
		}
		return testResponse(request, http.StatusTooManyRequests, `{}`, http.Header{"Retry-After": {"90"}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Validate(context.Background(), retryAfterTestSession())
	var typed *Error
	if !errors.As(err, &typed) || !typed.Retryable || !IsRateLimited(err) || typed.RetryAfter != 90*time.Second || RetryDelay(err) != 90*time.Second || requests != 1 {
		t.Fatalf("validate error = %#v, delay = %v, requests = %d", typed, RetryDelay(err), requests)
	}
}

func TestCreateAliasReserveRetryAfterDoesNotReplay(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			generateRequests, reserveRequests := 0, 0
			client, err := NewClient(Config{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v1/hme/generate":
					generateRequests++
					return testResponse(request, http.StatusOK, `{"success":true,"result":{"hme":"candidate@icloud.com"}}`, http.Header{"Retry-After": {"999"}}), nil
				case "/v1/hme/reserve":
					reserveRequests++
					return testResponse(request, status, `{"success":false,"error":{"errorCode":-41015}}`, http.Header{"Retry-After": {"120"}}), nil
				default:
					t.Fatalf("unexpected request path %q", request.URL.Path)
					return nil, errors.New("unexpected request")
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			alias, _, err := client.CreateAlias(context.Background(), retryAfterTestSession(), "label", "note")
			var typed *Error
			if !errors.As(err, &typed) || typed.Retryable || typed.RetryAfter != 2*time.Minute || !IsRateLimited(err) {
				t.Fatalf("reserve error = %#v", typed)
			}
			if alias != (Alias{}) || generateRequests != 1 || reserveRequests != 1 {
				t.Fatalf("alias = %#v, generate/reserve = %d/%d", alias, generateRequests, reserveRequests)
			}
		})
	}
}

func TestRetryAfterSurvivesRedirectFailure(t *testing.T) {
	requests := 0
	client, err := NewClient(Config{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return testResponse(request, http.StatusFound, "", http.Header{
			"Location":    {"https://invalid.example/redirect"},
			"Retry-After": {"90"},
		}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DeleteAlias(context.Background(), retryAfterTestSession(), "remote-id")
	var typed *Error
	if !errors.As(err, &typed) || typed.Retryable || typed.StatusCode != http.StatusFound || typed.RetryAfter != 90*time.Second || requests != 1 {
		t.Fatalf("redirect error = %#v, requests = %d", typed, requests)
	}
}

func retryAfterTestSession() Session {
	return Session{
		Region: RegionGlobal, DSID: "42", ClientID: "client-id",
		PremiumMailSettingsURL: "https://p01-maildomainws.icloud.com",
	}
}

type retryAfterFailingBody struct {
	closed bool
}

func (body *retryAfterFailingBody) Read(p []byte) (int, error) {
	return copy(p, "partial-body-secret"), io.ErrUnexpectedEOF
}

func (body *retryAfterFailingBody) Close() error {
	body.closed = true
	return nil
}
