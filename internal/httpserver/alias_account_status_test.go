package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestAliasListFollowsPrimaryStatusWithoutChangingIndividualSwitches(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "alias-parent@icloud.com")
	active, _ := createV2AliasFixture(t, env, account.ID, "parent-active@icloud.com")
	paused, _ := createV2AliasFixture(t, env, account.ID, "parent-paused@icloud.com")
	if err := env.store.EnrollPoolAliases(ctx, []int64{active.ID, paused.ID}); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetPoolMember(ctx, paused.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAliasEnabled(ctx, paused.ID, false); err != nil {
		t.Fatal(err)
	}
	cookie, csrf, _ := env.createSession(t, "alias-parent-admin", "password")
	list := func(filter string) []adminAPIAliasDTO {
		t.Helper()
		response := env.request(t, http.MethodGet, "/admin/api/v1/aliases?enabled="+filter, nil, "", []*http.Cookie{cookie}, "")
		if response.Code != http.StatusOK {
			t.Fatalf("list aliases status=%d", response.Code)
		}
		var result struct {
			Data struct {
				Items []adminAPIAliasDTO `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data.Items
	}
	for _, enabled := range []bool{false, true, false, true} {
		body, err := json.Marshal(map[string]any{"name": account.Name, "email": account.Email,
			"imap_username": account.IMAPUsername, "enabled": enabled})
		if err != nil {
			t.Fatal(err)
		}
		update := env.request(t, http.MethodPut, fmt.Sprintf("/admin/api/v1/accounts/%d", account.ID), body, "application/json", []*http.Cookie{cookie}, csrf)
		if update.Code != http.StatusOK {
			t.Fatalf("toggle primary status=%d body=%s", update.Code, update.Body.String())
		}
		members, err := env.store.ListPoolMembers(ctx, "", "", 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		if enabled && members.Total != 2 || !enabled && members.Total != 0 {
			t.Fatalf("parent enabled=%v visible pool members=%d", enabled, members.Total)
		}
		for _, member := range members.Items {
			if member.AliasID == paused.ID && member.State != "paused" {
				t.Fatal("parent toggle restored a manually paused pool member as available")
			}
		}
		stored, err := env.store.GetAlias(ctx, active.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !stored.Enabled || stored.AccountDisabled == enabled {
			t.Fatalf("parent=%v raw alias enabled=%v parent disabled=%v", enabled, stored.Enabled, stored.AccountDisabled)
		}
		dto, err := env.server.adminAPIAliasFromDomain(stored)
		if err != nil {
			t.Fatal(err)
		}
		if dto.Enabled != enabled || !dto.ConfiguredEnabled || dto.AccountEnabled != enabled {
			t.Fatalf("parent=%v effective=%v configured=%v account=%v", enabled, dto.Enabled, dto.ConfiguredEnabled, dto.AccountEnabled)
		}
		individuallyPaused, err := env.store.GetAlias(ctx, paused.ID)
		if err != nil {
			t.Fatal(err)
		}
		if individuallyPaused.Enabled {
			t.Fatal("parent toggle enabled a manually paused alias")
		}
		if enabled {
			if rows := list("true"); len(rows) != 1 || rows[0].ID != active.ID {
				t.Fatal("enabled filter did not restore the original active alias")
			}
			if rows := list("false"); len(rows) != 1 || rows[0].ID != paused.ID {
				t.Fatal("disabled filter lost the manually paused alias")
			}
		} else {
			if rows := list("true"); len(rows) != 0 {
				t.Fatal("disabled primary still has effectively enabled aliases")
			}
			if rows := list("false"); len(rows) != 0 {
				t.Fatal("disabled filter displayed aliases from a disabled primary")
			}
			if rows := list(""); len(rows) != 0 {
				t.Fatal("unfiltered list displayed aliases from a disabled primary")
			}
		}
		detail, err := env.server.adminAPIAccountDetail(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantVisible := 0
		if enabled {
			wantVisible = 2
		}
		if len(detail.Aliases) != wantVisible || detail.Pagination["total"] != wantVisible || detail.Account.AliasCount != 2 {
			t.Fatalf("parent=%v detail items=%d total=%v owned=%d", enabled, len(detail.Aliases), detail.Pagination["total"], detail.Account.AliasCount)
		}
	}
}

func TestPoolEnrollmentBlocksStaleSelectionFromDisabledPrimary(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	first := adminAPITestCreateAccount(t, env, "enrollment-first@icloud.com")
	second := adminAPITestCreateAccount(t, env, "enrollment-second@icloud.com")
	firstAlias, _ := createV2AliasFixture(t, env, first.ID, "enrollment-available@icloud.com")
	secondAlias, _ := createV2AliasFixture(t, env, second.ID, "enrollment-paused@icloud.com")
	second.Enabled = false
	if _, err := env.store.UpdateAccount(ctx, second); err != nil {
		t.Fatal(err)
	}
	cookie, csrf, _ := env.createSession(t, "enrollment-admin", "password")
	body, err := json.Marshal(map[string]any{"alias_ids": []int64{firstAlias.ID, secondAlias.ID}})
	if err != nil {
		t.Fatal(err)
	}
	request := func() int {
		return env.request(t, http.MethodPost, "/admin/api/v1/pool/members", body, "application/json", []*http.Cookie{cookie}, csrf).Code
	}
	if code := request(); code != http.StatusConflict {
		t.Fatalf("stale enrollment status=%d, want 409", code)
	}
	page, err := env.store.ListPoolMembers(ctx, "", "", 50, 0)
	if err != nil || page.Total != 0 {
		t.Fatalf("mixed invalid batch partially enrolled: total=%d err=%v", page.Total, err)
	}
	second.Enabled = true
	if _, err := env.store.UpdateAccount(ctx, second); err != nil {
		t.Fatal(err)
	}
	if code := request(); code != http.StatusOK {
		t.Fatalf("reenabled enrollment status=%d, want 200", code)
	}
	page, err = env.store.ListPoolMembers(ctx, "", "", 50, 0)
	if err != nil || page.Total != 2 {
		t.Fatalf("reenabled enrollment total=%d err=%v", page.Total, err)
	}
}
