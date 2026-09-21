package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

type creationJobFake struct {
	fakeHMESyncService
	calls  atomic.Int32
	create func(context.Context, int64, string) (domain.Alias, error)
}

func (f *creationJobFake) ProbeAliasWithChannel(ctx context.Context, id int64, ch string) (domain.Alias, error) {
	f.calls.Add(1)
	return f.create(ctx, id, ch)
}

func startCreationJobTest(t *testing.T, env *adminAPITestEnv) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := env.server.StartAliasCreationJobs(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); env.server.aliasCreationJobs.wg.Wait() })
}

func waitCreationJob(t *testing.T, s *store.Store, account int64, status string) store.AliasCreationJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, e := s.GetLatestAliasCreationJob(context.Background(), account)
		if e == nil && j.Status == status {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	j, err := s.GetLatestAliasCreationJob(context.Background(), account)
	t.Fatalf("waiting for %s: job=%+v error=%v", status, j, err)
	return store.AliasCreationJob{}
}

func TestAliasCreationJobCompletes30(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "job30@icloud.com")
	cookie, csrf, _ := env.createSession(t, "creator", "password")
	var n atomic.Int64
	f := &creationJobFake{create: func(_ context.Context, id int64, ch string) (domain.Alias, error) {
		i := n.Add(1)
		return domain.Alias{ID: i, AccountID: id, Address: fmt.Sprintf("alias%d@icloud.com", i), Enabled: true}, nil
	}}
	env.server.SetHMESyncService(f)
	startCreationJobTest(t, env)
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/creation-job", account.ID)
	response := env.request(t, http.MethodPost, path, []byte(`{"count":30,"channel":"auto"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 202 {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	got := waitCreationJob(t, env.store, account.ID, "completed")
	if got.Completed != 30 || len(got.Entries) != 30 || f.calls.Load() != 30 {
		t.Fatalf("job=%+v calls=%d", got, f.calls.Load())
	}
}

func TestAliasCreationJobStopsInflight(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "stop@icloud.com")
	cookie, csrf, _ := env.createSession(t, "creator", "password")
	entered, release := make(chan struct{}), make(chan struct{})
	f := &creationJobFake{create: func(ctx context.Context, id int64, ch string) (domain.Alias, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return domain.Alias{}, ctx.Err()
		}
		return domain.Alias{ID: 1, AccountID: id, Address: "one@icloud.com", Enabled: true}, nil
	}}
	env.server.SetHMESyncService(f)
	startCreationJobTest(t, env)
	defer close(release)
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/creation-job", account.ID)
	response := env.request(t, http.MethodPost, path, []byte(`{"count":30,"channel":"auto"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 202 {
		t.Fatalf("start: %d", response.Code)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("creation not entered")
	}
	response = env.request(t, http.MethodPost, path, []byte(`{"count":2,"channel":"auto"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 409 {
		t.Fatalf("concurrent start: %d", response.Code)
	}
	response = env.request(t, http.MethodPost, path+"/stop", []byte(`{}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 200 {
		t.Fatalf("stop: %d", response.Code)
	}
	// The in-flight worker owns the terminal checkpoint even after stop returns.
	j, err := env.store.GetLatestAliasCreationJob(context.Background(), account.ID)
	if err != nil || j.Status != "running" {
		t.Fatalf("premature terminal state: %+v %v", j, err)
	}
	release <- struct{}{}
	j = waitCreationJob(t, env.store, account.ID, "stopped")
	if j.Completed != 1 || len(j.Entries) != 1 || f.calls.Load() != 1 {
		t.Fatalf("job=%+v calls=%d", j, f.calls.Load())
	}
}

func TestAliasCreationJobRateWaitCanStop(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "rate@icloud.com")
	cookie, csrf, _ := env.createSession(t, "creator", "password")
	f := &creationJobFake{create: func(context.Context, int64, string) (domain.Alias, error) {
		return domain.Alias{}, &apple.Error{Kind: apple.ErrService, StatusCode: 429}
	}}
	env.server.SetHMESyncService(f)
	startCreationJobTest(t, env)
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/creation-job", account.ID)
	response := env.request(t, http.MethodPost, path, []byte(`{"count":30,"channel":"auto"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 202 {
		t.Fatalf("start: %d", response.Code)
	}
	j := waitCreationJob(t, env.store, account.ID, "waiting")
	if j.NextRunAt == nil || time.Until(*j.NextRunAt) < time.Hour-time.Second || time.Until(*j.NextRunAt) > time.Hour+time.Second {
		t.Fatalf("cooldown: %+v", j)
	}
	response = env.request(t, http.MethodPost, path+"/stop", []byte(`{}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 200 {
		t.Fatalf("stop: %d", response.Code)
	}
	j = waitCreationJob(t, env.store, account.ID, "stopped")
	if j.Completed != 0 || f.calls.Load() != 1 || j.NextRunAt != nil {
		t.Fatalf("job=%+v calls=%d", j, f.calls.Load())
	}
}

func TestAliasCreationJobStopsForIMAPAuthenticationPause(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "imap-paused-job@icloud.com")
	cookie, csrf, _ := env.createSession(t, "imap-paused-job-admin", "password")
	f := &creationJobFake{create: func(context.Context, int64, string) (domain.Alias, error) {
		return domain.Alias{}, domain.ErrIMAPAuthenticationPaused
	}}
	env.server.SetHMESyncService(f)
	startCreationJobTest(t, env)
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/creation-job", account.ID)
	response := env.request(t, http.MethodPost, path, []byte(`{"count":3,"channel":"auto"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != 202 {
		t.Fatalf("start: %d", response.Code)
	}
	j := waitCreationJob(t, env.store, account.ID, "failed")
	if f.calls.Load() != 1 || j.NextRunAt != nil || j.LastError != domain.ErrIMAPAuthenticationPaused.Error() {
		t.Fatalf("IMAP pause was retried or hidden: calls=%d job=%+v", f.calls.Load(), j)
	}
}

func TestAliasCreationJobAccessAndValidation(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "access@icloud.com")
	cookie, csrf, _ := env.createSession(t, "creator", "password")
	startCreationJobTest(t, env)
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/creation-job", account.ID)
	for _, tc := range []struct {
		name, body, csrf string
		cookie           bool
		status           int
	}{
		{"anonymous", `{"count":30,"channel":"auto"}`, "", false, 401},
		{"csrf", `{"count":30,"channel":"auto"}`, "", true, 403},
		{"too many", `{"count":101,"channel":"auto"}`, csrf, true, 400},
		{"zero", `{"count":0,"channel":"auto"}`, csrf, true, 400},
		{"channel", `{"count":30,"channel":"invalid"}`, csrf, true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cookies []*http.Cookie
			if tc.cookie {
				cookies = []*http.Cookie{cookie}
			}
			response := env.request(t, http.MethodPost, path, []byte(tc.body), "application/json", cookies, tc.csrf)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
