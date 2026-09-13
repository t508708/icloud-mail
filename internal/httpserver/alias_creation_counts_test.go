package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"icloud-api/internal/autocreate"
	"icloud-api/internal/domain"
)

func TestAdminAPIAutoCreationCountsUseLocalDayAndAccountWindow(t *testing.T) {
	env := newAdminAPITestEnv(t)
	now := time.Date(2026, 9, 13, 8, 48, 15, 0, time.Local)
	env.server.now = func() time.Time { return now }
	a1 := adminAPITestCreateAccount(t, env, "counts-one@icloud.com")
	a2 := adminAPITestCreateAccount(t, env, "counts-two@icloud.com")
	ctx := context.Background()
	today := time.Date(2026, 9, 13, 0, 0, 0, 0, time.Local)
	insert := func(accountID int64, address string, at time.Time) {
		t.Helper()
		if _, err := env.store.DB().ExecContext(ctx, `INSERT INTO apple_alias_creation_events(account_id,address,created_at) VALUES(?,?,?)`, accountID, address, at.UnixNano()); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 89; i++ {
		insert(a1.ID, fmt.Sprintf("count-%d@icloud.com", i), today.Add(time.Duration(i)*time.Minute))
	}
	insert(a1.ID, "yesterday@icloud.com", today.Add(-time.Minute))
	insert(a1.ID, "future@icloud.com", now.Add(time.Hour))
	insert(a2.ID, "other@icloud.com", today.Add(time.Minute))
	recent, since, daily, midnight, err := env.server.adminAPIAutoCreationCounts(ctx, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recent != 0 || daily != 89 || !since.Equal(now.Add(-time.Hour)) || !midnight.Equal(today) {
		t.Fatalf("counts=%d,%d windows=%v,%v want 0,89 with local midnight", recent, daily, since, midnight)
	}
	// Reading the detail and toggling a plan must return identical count windows.
	auto, err := autocreate.New(env.store, func(context.Context, int64) (domain.Alias, error) {
		t.Fatal("count read triggered creation")
		return domain.Alias{}, nil
	}, env.server.logger)
	if err != nil {
		t.Fatal(err)
	}
	env.server.SetAliasAutoCreationService(auto)
	cookie, csrf, _ := env.createSession(t, "counts-admin", "password")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		path := fmt.Sprintf("/admin/api/v1/accounts/%d", a1.ID)
		var body []byte
		if method == http.MethodPut {
			path += "/aliases/auto-create"
			body = []byte(`{"enabled":false}`)
		}
		response := env.request(t, method, path, body, "application/json", []*http.Cookie{cookie}, csrf)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body.String())
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		var dto adminAPIAutoCreationDTO
		if method == http.MethodGet {
			var detail adminAPIAccountDetailDTO
			if err := json.Unmarshal(envelope.Data, &detail); err != nil {
				t.Fatal(err)
			}
			if detail.AutoCreation == nil {
				t.Fatal("missing automatic state")
			}
			dto = *detail.AutoCreation
		} else if err := json.Unmarshal(envelope.Data, &dto); err != nil {
			t.Fatal(err)
		}
		if dto.RecentCreatedCount != 0 || dto.TodayCreatedCount != 89 || dto.TodayCreatedSince != adminAPITime(today) || dto.RecentCreatedSince != adminAPITime(now.Add(-time.Hour)) {
			t.Fatalf("%s counts differ: %+v", method, dto)
		}
	}
	// The rolling hour may include yesterday while today's window must not.
	now = today.Add(30 * time.Minute)
	recent, _, daily, _, err = env.server.adminAPIAutoCreationCounts(ctx, a1.ID)
	if err != nil || recent != 32 || daily != 31 {
		t.Fatalf("cross-day counts=%d,%d err=%v want 32,31", recent, daily, err)
	}
	recent, _, daily, _, err = env.server.adminAPIAutoCreationCounts(ctx, a2.ID)
	if err != nil || recent != 1 || daily != 1 {
		t.Fatalf("account isolation counts=%d,%d err=%v", recent, daily, err)
	}
}
