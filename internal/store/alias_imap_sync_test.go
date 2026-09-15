package store_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestAliasMailboxSyncKeepsCursorsStatusesAndRecipientsIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, account, first, second := aliasIMAPSyncFixture(t)
	at := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
	applyArchiveV2Batch(t, ctx, db, account.ID, []domain.Alias{first, second}, nil, 91, 100, true)
	accountState, err := db.GetIMAPSyncState(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `
		UPDATE aliases SET last_sync_status = ?, last_sync_error = ? WHERE id = ?`,
		domain.SyncStatusError, "second alias failure", second.ID); err != nil {
		t.Fatal(err)
	}
	secondBefore := readAliasIMAPSyncAlias(t, ctx, db, second.ID)
	firstResult := aliasIMAPSyncResult(first, 91, 9, at)
	firstResult.HasMore = true
	applyAliasIMAPSyncBatch(t, ctx, db, first, firstResult, at)
	if state, err := db.GetAliasIMAPSyncState(ctx, first.ID); err != nil ||
		state.AccountID != account.ID || state.UIDValidity != 91 || state.LastUID != 9 || !state.UpdatedAt.Equal(at) {
		t.Fatalf("first alias cursor = %#v, %v", state, err)
	}
	if _, err := db.GetAliasIMAPSyncState(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second alias cursor before its fetch = %v, want ErrNotFound", err)
	}
	if got := readAliasIMAPSyncAlias(t, ctx, db, second.ID); !reflect.DeepEqual(got, secondBefore) {
		t.Fatalf("first fetch changed second alias: before=%#v after=%#v", secondBefore, got)
	}
	if got := readAliasIMAPSyncAlias(t, ctx, db, first.ID); got.LastSyncStatus != domain.SyncStatusPending {
		t.Fatalf("first alias status = %q, want pending", got.LastSyncStatus)
	}
	if _, err := db.GetLatestMessage(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("first fetch created second alias legacy snapshot: %v", err)
	}
	secondResult := aliasIMAPSyncResult(second, 91, 3, at.Add(time.Minute))
	applyAliasIMAPSyncBatch(t, ctx, db, second, secondResult, at.Add(time.Minute))
	secondState, err := db.GetAliasIMAPSyncState(ctx, second.ID)
	if err != nil || secondState.AccountID != account.ID || secondState.LastUID != 3 {
		t.Fatalf("second alias cursor = %#v, %v", secondState, err)
	}
	for _, alias := range []domain.Alias{first, second} {
		messages, err := db.ListArchivedMailboxMessages(ctx, alias.ID)
		if err != nil || len(messages) != 1 || messages[0].Subject != alias.Address {
			t.Fatalf("alias %d archive = %#v, %v", alias.ID, messages, err)
		}
		latest, err := db.GetLatestMessage(ctx, alias.ID)
		if err != nil || latest.Subject != alias.Address {
			t.Fatalf("alias %d latest = %#v, %v", alias.ID, latest, err)
		}
	}
	secondBefore = readAliasIMAPSyncAlias(t, ctx, db, second.ID)
	firstResult.State.LastUID = 12
	firstResult.State.UpdatedAt = at.Add(2 * time.Minute)
	firstResult.ArchivedMessages = nil
	firstResult.Reset = false
	firstResult.HasMore = false
	applyAliasIMAPSyncBatch(t, ctx, db, first, firstResult, at.Add(2*time.Minute))
	if got := readAliasIMAPSyncAlias(t, ctx, db, first.ID); got.LastSyncStatus != domain.SyncStatusOK {
		t.Fatalf("first alias status after catch-up = %q", got.LastSyncStatus)
	}
	if got := readAliasIMAPSyncAlias(t, ctx, db, second.ID); !reflect.DeepEqual(got, secondBefore) {
		t.Fatalf("first catch-up changed second alias: before=%#v after=%#v", secondBefore, got)
	}
	if got, err := db.GetAliasIMAPSyncState(ctx, second.ID); err != nil || got != secondState {
		t.Fatalf("first catch-up changed second cursor: %#v, %v", got, err)
	}
	if got, err := db.GetIMAPSyncState(ctx, account.ID); err != nil || got != accountState {
		t.Fatalf("targeted fetch changed account cursor: %#v, %v; before=%#v", got, err, accountState)
	}
}

func TestAliasMailboxSyncResetAndEmptySnapshotAffectOnlyTarget(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		generation    uint32
		deleteCursor  bool
		emptySnapshot bool
		wantLatest    bool
	}{
		{name: "new_generation", generation: 92},
		{name: "missing_cursor_new_generation", generation: 92, deleteCursor: true},
		{name: "same_generation", generation: 91, wantLatest: true},
		{name: "missing_cursor_same_generation", generation: 91, deleteCursor: true, wantLatest: true},
		{name: "authoritative_empty", generation: 91, emptySnapshot: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, account, first, second := aliasIMAPSyncFixture(t)
			at := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
			applyAliasIMAPSyncBatch(t, ctx, db, first, aliasIMAPSyncResult(first, 91, 9, at), at)
			applyAliasIMAPSyncBatch(t, ctx, db, second, aliasIMAPSyncResult(second, 91, 11, at), at)
			secondBefore := readAliasIMAPSyncAlias(t, ctx, db, second.ID)
			secondState, err := db.GetAliasIMAPSyncState(ctx, second.ID)
			if err != nil {
				t.Fatal(err)
			}
			secondLatest, err := db.GetLatestMessage(ctx, second.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.deleteCursor {
				if _, err := db.DB().ExecContext(ctx, `DELETE FROM alias_imap_sync_states WHERE alias_id = ?`, first.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := domain.MailboxSyncResult{
				State: domain.IMAPSyncState{AccountID: account.ID, UIDValidity: test.generation, LastUID: 9, UpdatedAt: at.Add(time.Minute)},
				Reset: true,
			}
			if test.emptySnapshot {
				result.Reset = false
				result.LegacySnapshotUpdates = map[int64]domain.LatestMessage{
					first.ID: {AliasID: first.ID, UIDValidity: test.generation, SnapshotState: domain.SnapshotEmpty},
				}
			}
			applyAliasIMAPSyncBatch(t, ctx, db, first, result, at.Add(time.Minute))
			firstLatest, err := db.GetLatestMessage(ctx, first.ID)
			if test.wantLatest {
				if err != nil || firstLatest.UID != 9 || firstLatest.UIDValidity != 91 {
					t.Fatalf("preserved first latest = %#v, %v", firstLatest, err)
				}
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("reset first latest = %#v, %v, want ErrNotFound", firstLatest, err)
			}
			if got := readAliasIMAPSyncAlias(t, ctx, db, second.ID); !reflect.DeepEqual(got, secondBefore) {
				t.Fatalf("first reset changed second alias: before=%#v after=%#v", secondBefore, got)
			}
			if got, err := db.GetLatestMessage(ctx, second.ID); err != nil || !reflect.DeepEqual(got, secondLatest) {
				t.Fatalf("first reset changed second latest: %#v, %v", got, err)
			}
			if got, err := db.GetAliasIMAPSyncState(ctx, second.ID); err != nil || got != secondState {
				t.Fatalf("first reset changed second cursor: %#v, %v", got, err)
			}
			for _, alias := range []domain.Alias{first, second} {
				if messages, err := db.ListArchivedMailboxMessages(ctx, alias.ID); err != nil || len(messages) != 1 {
					t.Fatalf("alias %d archive after reset = %#v, %v", alias.ID, messages, err)
				}
			}
		})
	}
}

func TestAliasMailboxSyncRejectsCrossAliasResults(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"archive_recipient", "legacy_empty", "wrong_account", "disabled_input", "missing_version"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, account, first, second := aliasIMAPSyncFixture(t)
			at := time.Date(2026, 9, 13, 3, 0, 0, 0, time.UTC)
			result := aliasIMAPSyncResult(first, 91, 9, at)
			account, err := db.GetAccount(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			version := account.UpdatedAt
			switch name {
			case "archive_recipient":
				result.ArchivedMessages[0].AliasIDs = []int64{first.ID, second.ID}
			case "legacy_empty":
				result.LegacySnapshotUpdates = map[int64]domain.LatestMessage{
					second.ID: {AliasID: second.ID, UIDValidity: 91, SnapshotState: domain.SnapshotEmpty},
				}
			case "wrong_account":
				result.State.AccountID++
			case "disabled_input":
				first.Enabled = false
			case "missing_version":
				version = time.Time{}
			}
			if err := db.ApplyAliasMailboxSync(ctx, version, first, result, at); err == nil {
				t.Fatal("invalid targeted publication succeeded")
			}
			for _, alias := range []domain.Alias{first, second} {
				if _, err := db.GetAliasIMAPSyncState(ctx, alias.ID); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("invalid result created alias %d cursor: %v", alias.ID, err)
				}
				if messages, err := db.ListArchivedMailboxMessages(ctx, alias.ID); err != nil || len(messages) != 0 {
					t.Fatalf("invalid result created alias %d messages: %#v, %v", alias.ID, messages, err)
				}
			}
		})
	}
}

func TestAliasMailboxSyncSkipsStaleAndDisabledPublications(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"old_account_version", "account_disabled", "alias_disabled", "alias_address_changed",
		"lower_uid", "older_generation", "equal_time_generation", "generation_without_reset", "missing_cursor_incremental",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, account, alias, _ := aliasIMAPSyncFixture(t)
			at := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
			account, err := db.GetAccount(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			oldVersion := account.UpdatedAt
			applyAliasIMAPSyncBatch(t, ctx, db, alias, aliasIMAPSyncResult(alias, 91, 9, at), at)
			state, err := db.GetAliasIMAPSyncState(ctx, alias.ID)
			if err != nil {
				t.Fatal(err)
			}
			latest, err := db.GetLatestMessage(ctx, alias.ID)
			if err != nil {
				t.Fatal(err)
			}
			account, err = db.GetAccount(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			version := account.UpdatedAt
			result := aliasIMAPSyncResult(alias, 91, 10, at.Add(time.Minute))
			result.Reset = false
			switch name {
			case "old_account_version":
				version = oldVersion
			case "account_disabled":
				_, err = db.DB().ExecContext(ctx, `UPDATE accounts SET enabled = FALSE WHERE id = ?`, account.ID)
			case "alias_disabled":
				_, err = db.DB().ExecContext(ctx, `UPDATE aliases SET enabled = FALSE WHERE id = ?`, alias.ID)
			case "alias_address_changed":
				_, err = db.DB().ExecContext(ctx, `UPDATE aliases SET address = ? WHERE id = ?`, "changed@icloud.com", alias.ID)
			case "lower_uid":
				result.State.LastUID = 8
				result.ArchivedMessages[0].UID = 8
			case "older_generation", "equal_time_generation", "generation_without_reset":
				result.State.UIDValidity = 90
				result.ArchivedMessages[0].UIDValidity = 90
				result.Reset = name != "generation_without_reset"
				if name == "older_generation" {
					result.State.UpdatedAt = at.Add(-time.Minute)
				} else if name == "equal_time_generation" {
					result.State.UpdatedAt = at
				}
			case "missing_cursor_incremental":
				_, err = db.DB().ExecContext(ctx, `DELETE FROM alias_imap_sync_states WHERE alias_id = ?`, alias.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			aliasBefore := readAliasIMAPSyncAlias(t, ctx, db, alias.ID)
			if err := db.ApplyAliasMailboxSync(ctx, version, alias, result, at.Add(time.Minute)); !errors.Is(err, store.ErrAliasMailboxSyncStale) {
				t.Fatalf("skip stale publication = %v, want ErrAliasMailboxSyncStale", err)
			}
			got, err := db.GetAliasIMAPSyncState(ctx, alias.ID)
			if name == "missing_cursor_incremental" {
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("incremental result recreated missing cursor: %#v, %v", got, err)
				}
			} else if err != nil || got != state {
				t.Fatalf("skipped publication changed cursor: %#v, %v; before=%#v", got, err, state)
			}
			if got, err := db.GetLatestMessage(ctx, alias.ID); err != nil || !reflect.DeepEqual(got, latest) {
				t.Fatalf("skipped publication changed latest: %#v, %v", got, err)
			}
			if got := readAliasIMAPSyncAlias(t, ctx, db, alias.ID); !reflect.DeepEqual(got, aliasBefore) {
				t.Fatalf("skipped publication changed alias: before=%#v after=%#v", aliasBefore, got)
			}
			if messages, err := db.ListArchivedMailboxMessages(ctx, alias.ID); err != nil || len(messages) != 1 {
				t.Fatalf("skipped publication changed archive: %#v, %v", messages, err)
			}
		})
	}
}

func TestAliasMailboxSyncReportsStaleForMissingAlias(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, account, alias, second := aliasIMAPSyncFixture(t)
	at := time.Date(2026, 9, 13, 4, 30, 0, 0, time.UTC)
	secondBefore := readAliasIMAPSyncAlias(t, ctx, db, second.ID)
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM aliases WHERE id = ?`, alias.ID); err != nil {
		t.Fatal(err)
	}
	account, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyAliasMailboxSync(ctx, account.UpdatedAt, alias, aliasIMAPSyncResult(alias, 91, 9, at), at); !errors.Is(err, store.ErrAliasMailboxSyncStale) {
		t.Fatalf("publish missing alias = %v, want ErrAliasMailboxSyncStale", err)
	}
	if _, err := db.GetAliasIMAPSyncState(ctx, alias.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing alias publication created cursor: %v", err)
	}
	if stats, err := db.MailArchiveStats(ctx); err != nil || stats.MessageCount != 0 {
		t.Fatalf("missing alias publication created archive: %#v, %v", stats, err)
	}
	if got := readAliasIMAPSyncAlias(t, ctx, db, second.ID); !reflect.DeepEqual(got, secondBefore) {
		t.Fatalf("missing alias publication changed sibling: before=%#v after=%#v", secondBefore, got)
	}
	if got, err := db.GetAccount(ctx, account.ID); err != nil || !reflect.DeepEqual(got, account) {
		t.Fatalf("missing alias publication changed account: %#v, %v", got, err)
	}
}

func TestAliasMailboxSyncReplayPreservesConsumptionAndLocalUID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, account, alias, _ := aliasIMAPSyncFixture(t)
	at := time.Date(2026, 9, 13, 5, 0, 0, 0, time.UTC)
	result := aliasIMAPSyncResult(alias, 91, 9, at)
	applyAliasIMAPSyncBatch(t, ctx, db, alias, result, at)
	consume := func(want bool) {
		t.Helper()
		current := readAliasIMAPSyncAlias(t, ctx, db, alias.ID)
		latest, err := db.GetLatestMessage(ctx, alias.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.LastSyncedAt == nil {
			t.Fatal("synced alias has no sync time")
		}
		consumed, err := db.ConsumeLatestMessage(ctx, alias.ID, current.APIKeyHash,
			*current.LastSyncedAt, latest.SyncedAt, latest.UIDValidity, latest.UID, at.Add(2*time.Minute))
		if err != nil || consumed != want {
			t.Fatalf("consume latest = %t, %v, want %t", consumed, err, want)
		}
	}
	consume(true)
	result.Reset = false
	result.State.UpdatedAt = at.Add(time.Minute)
	result.ArchivedMessages[0].Subject = "refreshed metadata"
	applyAliasIMAPSyncBatch(t, ctx, db, alias, result, at.Add(time.Minute))
	consume(false)
	if messages, err := db.ListArchivedMailboxMessages(ctx, alias.ID); err != nil || len(messages) != 1 ||
		messages[0].MailboxUID != 1 || messages[0].Subject != "refreshed metadata" {
		t.Fatalf("replayed archive = %#v, %v", messages, err)
	}
	if got := readAliasIMAPSyncAlias(t, ctx, db, alias.ID); got.MailboxUIDNext != 2 {
		t.Fatalf("replay allocated another local UID: next=%d", got.MailboxUIDNext)
	}
	if tasks, err := db.ListSeenTasks(ctx, account.ID, 10); err != nil || len(tasks) != 1 || tasks[0].UID != 9 {
		t.Fatalf("seen tasks after replay = %#v, %v", tasks, err)
	}
	if _, err := db.GetIMAPSyncState(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("targeted fetch created account cursor: %v", err)
	}
}

func TestAliasMailboxSyncUsesUIDAndObservationRatherThanGenerationOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _, alias, _ := aliasIMAPSyncFixture(t)
	at := time.Date(2026, 9, 13, 5, 30, 0, 0, time.UTC)
	applyAliasIMAPSyncBatch(t, ctx, db, alias, aliasIMAPSyncResult(alias, 91, 9, at), at)

	progress := aliasIMAPSyncResult(alias, 91, 10, at.Add(-time.Minute))
	progress.Reset = false
	applyAliasIMAPSyncBatch(t, ctx, db, alias, progress, at.Add(time.Minute))
	if state, err := db.GetAliasIMAPSyncState(ctx, alias.ID); err != nil || state.LastUID != 10 || !state.UpdatedAt.Equal(at) {
		t.Fatalf("same-generation clock rollback lost cursor progress: %#v, %v", state, err)
	}

	next := aliasIMAPSyncResult(alias, 42, 1, at.Add(2*time.Minute))
	applyAliasIMAPSyncBatch(t, ctx, db, alias, next, at.Add(2*time.Minute))
	if state, err := db.GetAliasIMAPSyncState(ctx, alias.ID); err != nil || state.UIDValidity != 42 || state.LastUID != 1 {
		t.Fatalf("fresh lower-valued UIDVALIDITY was not accepted: %#v, %v", state, err)
	}
	if latest, err := db.GetLatestMessage(ctx, alias.ID); err != nil || latest.UIDValidity != 42 || latest.UID != 1 {
		t.Fatalf("fresh generation legacy snapshot = %#v, %v", latest, err)
	}
}

func TestGetAliasIMAPSyncStateValidatesPositionAndCascadesDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _, alias, _ := aliasIMAPSyncFixture(t)
	if _, err := db.GetAliasIMAPSyncState(ctx, 0); err == nil {
		t.Fatal("zero alias ID accepted")
	}
	at := time.Date(2026, 9, 13, 6, 0, 0, 0, time.UTC)
	applyAliasIMAPSyncBatch(t, ctx, db, alias, aliasIMAPSyncResult(alias, 91, 9, at), at)
	for _, position := range [][2]int64{{0, 9}, {1 << 32, 9}, {91, -1}, {91, 1 << 32}} {
		if _, err := db.DB().ExecContext(ctx, `
			UPDATE alias_imap_sync_states SET uid_validity = ?, last_uid = ? WHERE alias_id = ?`,
			position[0], position[1], alias.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetAliasIMAPSyncState(ctx, alias.ID); err == nil {
			t.Fatalf("invalid position accepted: %v", position)
		}
	}
	if _, err := db.DB().ExecContext(ctx, `DELETE FROM aliases WHERE id = ?`, alias.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM alias_imap_sync_states WHERE alias_id = ?`, alias.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("alias cursor after deletion = %d rows, %v", count, err)
	}
}

func aliasIMAPSyncFixture(t *testing.T) (*store.Store, domain.Account, domain.Alias, domain.Alias) {
	t.Helper()
	ctx := context.Background()
	db, _ := openArchiveV2Store(t, 1<<20)
	account := createAccount(t, ctx, db, "Alias IMAP sync", "alias-imap-sync@icloud.com")
	first := createLegacyAlias(t, ctx, db, account.ID, "alias-imap-first@icloud.com", bytes.Repeat([]byte{0x61}, 32))
	second := createLegacyAlias(t, ctx, db, account.ID, "alias-imap-second@icloud.com", bytes.Repeat([]byte{0x62}, 32))
	return db, account, first, second
}

func aliasIMAPSyncResult(alias domain.Alias, uidValidity, uid uint32, at time.Time) domain.MailboxSyncResult {
	return domain.MailboxSyncResult{
		State: domain.IMAPSyncState{AccountID: alias.AccountID, UIDValidity: uidValidity, LastUID: uid, UpdatedAt: at},
		Reset: true,
		ArchivedMessages: []domain.ArchivedMessage{{
			AccountID: alias.AccountID, UIDValidity: uidValidity, UID: uid, InternalDate: at,
			Subject: alias.Address, ContentState: domain.ArchiveContentMetadata, AliasIDs: []int64{alias.ID},
		}},
	}
}

func applyAliasIMAPSyncBatch(t *testing.T, ctx context.Context, db *store.Store, alias domain.Alias, result domain.MailboxSyncResult, at time.Time) {
	t.Helper()
	account, err := db.GetAccount(ctx, alias.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyAliasMailboxSync(ctx, account.UpdatedAt, alias, result, at); err != nil {
		t.Fatalf("apply alias mailbox sync: %v", err)
	}
}

func readAliasIMAPSyncAlias(t *testing.T, ctx context.Context, db *store.Store, aliasID int64) domain.Alias {
	t.Helper()
	alias, err := db.GetAlias(ctx, aliasID)
	if err != nil {
		t.Fatal(err)
	}
	return alias
}
