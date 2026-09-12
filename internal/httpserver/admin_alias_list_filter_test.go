package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"icloud-api/internal/domain"
)

type adminAPIAliasListTestEnvelope struct {
	Data struct {
		Items      []adminAPIAliasDTO `json:"items"`
		Pagination struct {
			Limit   int  `json:"limit"`
			Offset  int  `json:"offset"`
			Total   int  `json:"total"`
			HasMore bool `json:"has_more"`
		} `json:"pagination"`
	} `json:"data"`
}

func TestAdminAPIListAliasesFiltersWithoutLatestMail(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "without-mail-admin", "unused-password")
	first := adminAPITestCreateAccount(t, env, "first-api-without-mail@icloud.com")
	second := adminAPITestCreateAccount(t, env, "second-api-without-mail@icloud.com")

	fixtures := []struct {
		accountID int64
		address   string
		label     string
	}{
		{accountID: first.ID, address: "alpha-api-empty@icloud.com", label: "Campaign target"},
		{accountID: first.ID, address: "bravo-api-with-mail@icloud.com", label: "Campaign target"},
		{accountID: first.ID, address: "charlie-api-empty@icloud.com", label: "Campaign target"},
		{accountID: first.ID, address: "delta-api-empty@icloud.com", label: "Personal"},
		{accountID: second.ID, address: "echo-api-empty@icloud.com", label: "Campaign target"},
	}
	aliases := make(map[string]domain.Alias, len(fixtures))
	for _, fixture := range fixtures {
		alias, err := env.store.CreateAlias(context.Background(), domain.Alias{
			AccountID: fixture.accountID,
			Address:   fixture.address,
			Label:     fixture.label,
			Enabled:   true,
		})
		if err != nil {
			t.Fatalf("create alias list fixture %q: %v", fixture.address, err)
		}
		aliases[fixture.address] = alias
	}
	adminAPITestSeedAliasArchivedMail(t, env, aliases["bravo-api-with-mail@icloud.com"],
		time.Date(2026, 8, 24, 10, 30, 0, 0, time.UTC))

	combinedTarget := fmt.Sprintf(
		"/admin/api/v1/aliases?without_latest_mail=true&account_id=%d&query=campaign&limit=1&offset=1",
		first.ID,
	)
	combined := decodeAdminAPIAliasListTestEnvelope(t, env.request(
		t, http.MethodGet, combinedTarget, nil, "", []*http.Cookie{cookie}, csrf,
	))
	if combined.Data.Pagination.Total != 2 || combined.Data.Pagination.Limit != 1 ||
		combined.Data.Pagination.Offset != 1 || combined.Data.Pagination.HasMore {
		t.Fatalf("combined without-mail pagination = %#v", combined.Data.Pagination)
	}
	if len(combined.Data.Items) != 1 || combined.Data.Items[0].Address != "charlie-api-empty@icloud.com" {
		t.Fatalf("combined without-mail items = %#v", combined.Data.Items)
	}
	if combined.Data.Items[0].LatestReceivedAt != nil {
		t.Fatalf("without-mail response has latest timestamp %v", combined.Data.Items[0].LatestReceivedAt)
	}

	falseTarget := fmt.Sprintf(
		"/admin/api/v1/aliases?without_latest_mail=false&account_id=%d&query=campaign&limit=20",
		first.ID,
	)
	withFilterDisabled := decodeAdminAPIAliasListTestEnvelope(t, env.request(
		t, http.MethodGet, falseTarget, nil, "", []*http.Cookie{cookie}, csrf,
	))
	if withFilterDisabled.Data.Pagination.Total != 3 || len(withFilterDisabled.Data.Items) != 3 {
		t.Fatalf("false without-mail filter response = %#v", withFilterDisabled.Data)
	}
	foundArchived := false
	for _, alias := range withFilterDisabled.Data.Items {
		if alias.Address == "bravo-api-with-mail@icloud.com" {
			foundArchived = alias.LatestReceivedAt != nil
		}
	}
	if !foundArchived {
		t.Fatalf("false without-mail filter omitted archived alias: %#v", withFilterDisabled.Data.Items)
	}

	omitted := decodeAdminAPIAliasListTestEnvelope(t, env.request(
		t, http.MethodGet, "/admin/api/v1/aliases?limit=20", nil, "", []*http.Cookie{cookie}, csrf,
	))
	if omitted.Data.Pagination.Total != len(fixtures) || len(omitted.Data.Items) != len(fixtures) {
		t.Fatalf("omitted without-mail filter response = %#v", omitted.Data)
	}

	standardTrue := decodeAdminAPIAliasListTestEnvelope(t, env.request(
		t, http.MethodGet, "/admin/api/v1/aliases?without_latest_mail=%20TRUE%20&limit=20", nil, "", []*http.Cookie{cookie}, csrf,
	))
	if standardTrue.Data.Pagination.Total != len(fixtures)-1 || len(standardTrue.Data.Items) != len(fixtures)-1 {
		t.Fatalf("standard true spelling response = %#v", standardTrue.Data)
	}
}

func TestAdminAPIListAliasesRejectsInvalidWithoutLatestMail(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "without-mail-validation-admin", "unused-password")

	for _, value := range []string{"", "yes", "2"} {
		t.Run(fmt.Sprintf("value_%q", value), func(t *testing.T) {
			target := "/admin/api/v1/aliases?without_latest_mail=" + url.QueryEscape(value)
			response := env.request(t, http.MethodGet, target, nil, "", []*http.Cookie{cookie}, csrf)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("without_latest_mail=%q status = %d, body=%s", value, response.Code, response.Body.String())
			}
			if code := adminAPITestErrorCode(t, response); code != "VALIDATION_FAILED" {
				t.Fatalf("without_latest_mail=%q error code = %q, want VALIDATION_FAILED", value, code)
			}
		})
	}
}

func TestAdminAPIListAliasesFiltersWithLatestMail(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "with-mail-admin", "unused-password")
	account := adminAPITestCreateAccount(t, env, "with-mail-filter-api@icloud.com")
	addresses := []string{
		"alpha-api-with-mail@icloud.com",
		"bravo-api-empty@icloud.com",
		"charlie-api-with-mail@icloud.com",
	}
	aliases := make(map[string]domain.Alias, len(addresses))
	for _, address := range addresses {
		alias, err := env.store.CreateAlias(context.Background(), domain.Alias{
			AccountID: account.ID,
			Address:   address,
			Label:     "Campaign target",
			Enabled:   true,
		})
		if err != nil {
			t.Fatalf("create with-mail API fixture %q: %v", address, err)
		}
		aliases[address] = alias
	}
	for _, address := range []string{addresses[0], addresses[2]} {
		adminAPITestSeedAliasArchivedMail(t, env, aliases[address],
			time.Date(2026, 8, 24, 10, 30, 0, 0, time.UTC))
	}

	target := fmt.Sprintf("/admin/api/v1/aliases?with_latest_mail=true&account_id=%d&query=campaign&limit=1&offset=1", account.ID)
	response := decodeAdminAPIAliasListTestEnvelope(t, env.request(
		t, http.MethodGet, target, nil, "", []*http.Cookie{cookie}, csrf,
	))
	if response.Data.Pagination.Total != 2 || response.Data.Pagination.Limit != 1 ||
		response.Data.Pagination.Offset != 1 || response.Data.Pagination.HasMore {
		t.Fatalf("with-mail pagination = %#v", response.Data.Pagination)
	}
	if len(response.Data.Items) != 1 || response.Data.Items[0].Address != addresses[2] {
		t.Fatalf("with-mail page items = %#v", response.Data.Items)
	}
	if response.Data.Items[0].LatestReceivedAt == nil {
		t.Fatal("with-mail response has nil latest timestamp")
	}

	for _, query := range []string{
		"with_latest_mail=false&account_id=" + fmt.Sprint(account.ID),
		"account_id=" + fmt.Sprint(account.ID),
	} {
		all := decodeAdminAPIAliasListTestEnvelope(t, env.request(
			t, http.MethodGet, "/admin/api/v1/aliases?limit=20&"+query, nil, "", []*http.Cookie{cookie}, csrf,
		))
		if all.Data.Pagination.Total != 3 || len(all.Data.Items) != 3 {
			t.Fatalf("unfiltered with-mail query %q = %#v", query, all.Data)
		}
	}
}

func TestAdminAPIListAliasesRejectsInvalidOrConflictingLatestMail(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "with-mail-validation-admin", "unused-password")
	for _, target := range []string{
		"/admin/api/v1/aliases?with_latest_mail=",
		"/admin/api/v1/aliases?with_latest_mail=yes",
		"/admin/api/v1/aliases?with_latest_mail=true&without_latest_mail=true",
	} {
		response := env.request(t, http.MethodGet, target, nil, "", []*http.Cookie{cookie}, csrf)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("latest-mail query %q status = %d, body=%s", target, response.Code, response.Body.String())
		}
		if code := adminAPITestErrorCode(t, response); code != "VALIDATION_FAILED" {
			t.Fatalf("latest-mail query %q error code = %q, want VALIDATION_FAILED", target, code)
		}
	}
}

func TestAdminAPIListAliasesFiltersEnabledAndRejectsInvalid(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "enabled-filter-admin", "unused-password")
	account := adminAPITestCreateAccount(t, env, "enabled-filter-api@icloud.com")
	for i, enabled := range []bool{false, true} {
		if _, err := env.store.CreateAlias(context.Background(), domain.Alias{AccountID: account.ID, Address: fmt.Sprintf("enabled-%d@icloud.com", i), Enabled: enabled}); err != nil {
			t.Fatal(err)
		}
	}
	target := fmt.Sprintf("/admin/api/v1/aliases?account_id=%d&enabled=false&limit=1", account.ID)
	page := decodeAdminAPIAliasListTestEnvelope(t, env.request(t, http.MethodGet, target, nil, "", []*http.Cookie{cookie}, csrf))
	if page.Data.Pagination.Total != 1 || len(page.Data.Items) != 1 || page.Data.Items[0].Enabled {
		t.Fatalf("enabled=false page = %#v", page.Data)
	}
	for _, value := range []string{"yes", "1"} {
		response := env.request(t, http.MethodGet, "/admin/api/v1/aliases?enabled="+value, nil, "", []*http.Cookie{cookie}, csrf)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("enabled=%s status=%d", value, response.Code)
		}
	}
}

func decodeAdminAPIAliasListTestEnvelope(
	t *testing.T,
	response *httptest.ResponseRecorder,
) adminAPIAliasListTestEnvelope {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("list aliases status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload adminAPIAliasListTestEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode alias list response: %v", err)
	}
	return payload
}

func adminAPITestSeedAliasArchivedMail(
	t *testing.T,
	env *adminAPITestEnv,
	alias domain.Alias,
	receivedAt time.Time,
) {
	t.Helper()
	ctx := context.Background()
	timestamp := receivedAt.UTC().UnixNano()
	var messageID int64
	if err := env.store.DB().QueryRowContext(ctx, `
		INSERT INTO archived_messages(
			account_id, uid_validity, upstream_uid, message_id,
			internal_date, synced_at, created_at
		) VALUES(?, ?, 1, ?, ?, ?, ?)
		RETURNING id`, alias.AccountID, alias.ID, fmt.Sprintf("<admin-filter-%s>", alias.Address), timestamp, timestamp, timestamp,
	).Scan(&messageID); err != nil {
		t.Fatalf("seed admin API archived message: %v", err)
	}
	if _, err := env.store.DB().ExecContext(ctx, `
		INSERT INTO alias_messages(alias_id, message_id, mailbox_uid, created_at)
		VALUES(?, ?, 1, ?)`, alias.ID, messageID, timestamp); err != nil {
		t.Fatalf("link admin API archived message: %v", err)
	}
}
