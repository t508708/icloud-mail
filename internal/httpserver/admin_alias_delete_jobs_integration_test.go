package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/hmesync"
)

type batchDeletionTestTransport func(*http.Request) (*http.Response, error)

func (transport batchDeletionTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

type batchDeletionTestLocker struct{}

func (batchDeletionTestLocker) WithAccountLock(ctx context.Context, _ int64, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

// Exercise the actual HTTP -> worker -> HME -> Apple client -> Store path,
// intercepting every Apple request in memory. No transport delegates to a network.
func TestAliasDeletionJobTwentyAndThousandContinuePastFourteenAfterDisconnect(t *testing.T) {
	for _, count := range []int{20, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			env := newAdminAPITestEnv(t)
			cookie, csrf, admin := env.createSession(t, "large-batch-admin", "unused-password")
			account := adminAPITestCreateAccount(t, env, "large-batch-owner@icloud.com")
			ids := make([]int64, count)
			directory := apple.ListResult{SelectedForwardTo: account.Email, ForwardToEmails: []string{account.Email}}
			remoteActive := make(map[string]bool, count)
			for i := range ids {
				alias := adminAPITestCreateDeleteAlias(t, env, account.ID, fmt.Sprintf("large-alias-%d@icloud.com", i))
				ids[i] = alias.ID
				remoteID := fmt.Sprint(alias.ID)
				directory.Aliases = append(directory.Aliases, apple.Alias{AnonymousID: remoteID, HME: alias.Address, ForwardToEmail: account.Email, IsActive: true})
				remoteActive[remoteID] = true
			}
			directoryJSON := adminAPITestJSON(t, map[string]any{"success": true, "result": directory})
			start, pastFourteen, continueBatch := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var requests, deleted atomic.Int32
			client, err := apple.NewClient(apple.Config{Transport: batchDeletionTestTransport(func(request *http.Request) (*http.Response, error) {
				requests.Add(1)
				body := `{"success":true}`
				switch request.URL.Path {
				case "/setup/ws/1/validate":
					select {
					case <-start:
					case <-request.Context().Done():
						return nil, request.Context().Err()
					}
					body = `{"dsInfo":{"dsid":"42","primaryEmail":"test-apple-owner@example.com","hsaVersion":2},"hsaTrustedBrowser":true,"webservices":{"premiummailsettings":{"url":"https://p01-maildomainws.icloud.com"}}}`
				case "/v2/hme/list":
					body = string(directoryJSON)
				case "/v1/hme/deactivate", "/v1/hme/delete":
					var payload struct {
						ID string `json:"anonymousId"`
					}
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						return nil, err
					}
					active, present := remoteActive[payload.ID]
					if !present {
						return nil, fmt.Errorf("unexpected fixture remote ID %s", payload.ID)
					}
					if request.URL.Path == "/v1/hme/deactivate" {
						remoteActive[payload.ID] = false
					} else {
						if active {
							return nil, fmt.Errorf("fixture deleted active address %s", payload.ID)
						}
						if deleted.Load() == 14 {
							close(pastFourteen)
							select {
							case <-continueBatch:
							case <-request.Context().Done():
								return nil, request.Context().Err()
							}
						}
						delete(remoteActive, payload.ID)
						deleted.Add(1)
					}
				default:
					return nil, fmt.Errorf("unexpected fixture path %s", request.URL.Path)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			// Advance a virtual clock for production pacing; no test sends a
			// real network request or waits a thousand real-world seconds.
			clockBase := time.Now().UTC()
			var clockOffset atomic.Int64
			service, err := hmesync.New(env.store, env.cipher, client, batchDeletionTestLocker{},
				hmesync.WithClock(func() time.Time { return clockBase.Add(time.Duration(clockOffset.Load())) }),
				hmesync.WithAliasDeletionWaiter(func(ctx context.Context, delay time.Duration) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					clockOffset.Add(int64(delay))
					return nil
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			session := apple.Session{AppleID: "test-apple-owner@example.com", Region: apple.RegionGlobal, DSID: "42", SessionToken: "fixture-token", ValidatedAt: time.Now().UTC()}
			encodedSession := adminAPITestJSON(t, session)
			ciphertext, err := env.cipher.EncryptAppleSession(string(encodedSession))
			if err != nil {
				t.Fatal(err)
			}
			_, err = env.store.UpsertAppleWebSession(context.Background(), domain.AppleWebSession{AccountID: account.ID, AppleID: session.AppleID, Region: "global", Authenticated: true, Ciphertext: ciphertext, LastValidatedAt: &session.ValidatedAt})
			if err != nil {
				t.Fatal(err)
			}
			env.server.SetHMESyncService(service)
			stop, stopped := startTestAliasDeletionJobs(t, env)
			body := adminAPITestJSON(t, map[string]any{"alias_ids": ids, "operation_id": testAliasDeletionJobID})
			requestContext, disconnect := context.WithCancel(context.Background())
			defer disconnect()
			request := httptest.NewRequest(http.MethodDelete, "http://admin.example.test/admin/api/v1/aliases/batch?async=1", bytes.NewReader(body)).WithContext(requestContext)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "http://admin.example.test")
			request.Header.Set(adminAPICSRFHeader, csrf)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			responseDone := make(chan struct{})
			go func() { env.router.ServeHTTP(response, request); close(responseDone) }()
			waitTestAliasDeletionSignal(t, responseDone)
			if response.Code != http.StatusAccepted {
				t.Fatalf("admission = %d %s", response.Code, response.Body.String())
			}
			disconnect()
			close(start)
			waitTestAliasDeletionSignal(t, pastFourteen)
			progress := env.request(t, http.MethodGet, "/admin/api/v1/aliases/batch/jobs/"+testAliasDeletionJobID, nil, "", []*http.Cookie{cookie}, "")
			partial := decodeTestAliasDeletionJob(t, progress)
			if progress.Code != http.StatusOK || partial.Deleted != 14 || partial.Processed != 14 || partial.Status != domain.AliasDeletionJobRunning {
				t.Fatalf("progress after original connection closed = %d %#v", progress.Code, partial)
			}
			close(continueBatch)
			// The 1000-item fixture repeatedly serializes durable job state; race
			// instrumentation needs a larger budget than a small-batch run.
			deadline := time.Now().Add(3 * time.Minute)
			var result adminAPIAliasDeletionJobDTO
			for {
				job, err := env.store.GetAliasDeletionJob(context.Background(), testAliasDeletionJobID, admin.ID)
				if err != nil {
					t.Fatal(err)
				}
				result = adminAPIAliasDeletionJobFromRecord(job)
				if !aliasDeletionJobActive(result.Status) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("fixture exceeded deadline at %d/%d", result.Processed, count)
				}
				time.Sleep(50 * time.Millisecond)
			}
			stop()
			waitTestAliasDeletionSignal(t, stopped)
			if result.Status != domain.AliasDeletionJobCompleted || result.Requested != count || result.Deleted != count || result.Processed != count || result.Failed != 0 || len(remoteActive) != 0 {
				t.Fatalf("large batch = status:%s requested:%d deleted:%d processed:%d failed:%d remote:%d", result.Status, result.Requested, result.Deleted, result.Processed, result.Failed, len(remoteActive))
			}
			if requests.Load() != int32(2*count+2) {
				t.Fatalf("Apple requests = %d, want %d", requests.Load(), 2*count+2)
			}
			var localAliases, audits int
			if err := env.store.DB().QueryRow("SELECT COUNT(*) FROM aliases WHERE account_id = ?", account.ID).Scan(&localAliases); err != nil {
				t.Fatal(err)
			}
			if err := env.store.DB().QueryRow("SELECT COUNT(*) FROM audit_logs WHERE request_id = ? AND result = 'success'", result.RequestID).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if localAliases != 0 || audits != count {
				t.Fatalf("durable results: aliases=%d audits=%d want=%d", localAliases, audits, count)
			}
		})
	}
}
