package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestPoolProjectSharingPreservesClaimsAndCredentials(t *testing.T) {
	db, _, account, aliases, original := poolFixture(t, 2, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "chatgpt-original-claim", Count: 1, AccountID: account.ID, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActPoolLease(ctx, leases[0].ID, original.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	usedAlias, err := db.GetAlias(ctx, leases[0].AliasID)
	if err != nil {
		t.Fatal(err)
	}
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	linked, added, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID)
	if err != nil || linked != len(aliases) || added != len(aliases) {
		t.Fatalf("first association: linked=%d added=%d err=%v", linked, added, err)
	}
	linked, added, err = db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID)
	if err != nil || linked != len(aliases) || added != 0 {
		t.Fatalf("repeat association: linked=%d added=%d err=%v", linked, added, err)
	}
	page, err := db.ListPoolProjectAliases(ctx, deepseek.ID, account.ID, 10, 0)
	if err != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("DeepSeek rows: %+v, %v", page, err)
	}
	for _, item := range page.Items {
		if item.Source != "shared" {
			t.Fatalf("unexpected project row source: %+v", item)
		}
	}
	page, err = db.ListPoolProjectAliases(ctx, original.ID, account.ID, 10, 0)
	if err != nil || page.Total != 1 || page.Items[0].AliasID != leases[0].AliasID || page.Items[0].Source != "claimed" {
		t.Fatalf("original claim missing: %+v, %v", page, err)
	}
	currentAlias, err := db.GetAlias(ctx, leases[0].AliasID)
	if err != nil || currentAlias.CredentialVersion != usedAlias.CredentialVersion || currentAlias.CredentialCiphertext != usedAlias.CredentialCiphertext {
		t.Fatal("sharing changed existing mailbox credentials", err)
	}
	lease, err := db.GetPoolLease(ctx, leases[0].ID, original.ID)
	if err != nil || lease.State != "used" {
		t.Fatal("original project lost its lease", err)
	}
	if _, err := db.PoolAliasRetirementAccounts(ctx, original.ID, []store.PoolAliasRetirementItemInput{{AliasID: usedAlias.ID, LeaseID: lease.ID}}); !errors.Is(err, store.ErrPoolRetirementShared) {
		t.Fatalf("shared alias should be protected from retirement: %v", err)
	}
	if err := db.UnlinkPoolProjectAlias(ctx, deepseek.ID, usedAlias.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.UnlinkPoolProjectAlias(ctx, deepseek.ID, usedAlias.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second unlink should not affect lease: %v", err)
	}
	page, err = db.ListPoolProjectAliases(ctx, deepseek.ID, account.ID, 10, 0)
	if err != nil || page.Total != 1 {
		t.Fatalf("remaining shared aliases: %+v, %v", page, err)
	}
	page, err = db.ListPoolProjectAliases(ctx, original.ID, account.ID, 10, 0)
	if err != nil || page.Total != 1 {
		t.Fatalf("original ownership changed: %+v, %v", page, err)
	}
}

func TestPoolProjectSharingRequiresEnabledProjectAndAccount(t *testing.T) {
	db, _, account, _, original := poolFixture(t, 1, "")
	ctx := context.Background()
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	account.Enabled = false
	if _, err := db.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID); !errors.Is(err, store.ErrPoolConflict) {
		t.Fatalf("disabled primary account shared: %v", err)
	}
	if _, err := db.UpdatePoolClient(ctx, original.ID, false, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LinkPoolProjectAccountAliases(ctx, original.ID, account.ID); !errors.Is(err, store.ErrPoolClosed) {
		t.Fatalf("disabled project linked account: %v", err)
	}
	if _, _, err := db.LinkPoolProjectAccountAliases(ctx, original.ID, 0); !errors.Is(err, store.ErrPoolInput) {
		t.Fatalf("invalid account ID accepted: %v", err)
	}
}

func TestPoolProjectSharingIncludesUnenrolledAliases(t *testing.T) {
	db, _, account, _, original := poolFixture(t, 1, "")
	ctx := context.Background()
	alias, err := db.CreateAlias(ctx, domain.Alias{
		AccountID: account.ID, Address: "never-enrolled@icloud.com", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	linked, added, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID)
	if err != nil || linked != 2 || added != 2 {
		t.Fatalf("unenrolled alias was skipped: linked=%d added=%d err=%v", linked, added, err)
	}
	page, err := db.ListPoolProjectAliases(ctx, deepseek.ID, account.ID, 10, 0)
	if err != nil || page.Total != 2 {
		t.Fatalf("unenrolled alias absent from association: %+v, %v", page, err)
	}
	if _, err := db.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "unenrolled-share", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatalf("shared addresses still claimable: %v", err)
	}
	if _, err := db.GetAlias(ctx, alias.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPoolSharedAvailableMailboxIsNotClaimable(t *testing.T) {
	db, _, account, aliases, original := poolFixture(t, 1, "")
	ctx := context.Background()
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID); err != nil {
		t.Fatal(err)
	}
	accounts, err := db.ListPoolClaimableAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Available != 0 {
		t.Fatalf("shared mailbox counted as available: %+v %v", accounts, err)
	}
	if _, err := db.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "shared-must-not-rotate", Count: 1, TTLSeconds: 600}); !errors.Is(err, store.ErrPoolEmpty) {
		t.Fatalf("shared mailbox was leased: %v", err)
	}
	if err := db.SetPoolMember(ctx, aliases[0].ID, "available"); !errors.Is(err, store.ErrPoolConflict) {
		t.Fatalf("shared mailbox marked available: %v", err)
	}
	if err := db.UnlinkPoolProjectAlias(ctx, deepseek.ID, aliases[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "unshared-can-be-leased", Count: 1, TTLSeconds: 600}); err != nil {
		t.Fatalf("unshared mailbox was not leased: %v", err)
	}
}

func TestPoolProjectSharingSkipsPendingLeases(t *testing.T) {
	db, _, account, _, original := poolFixture(t, 1, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "pending-share", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	if linked, added, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID); err != nil || linked != 0 || added != 0 {
		t.Fatalf("pending lease unexpectedly shared: %d %d %v", linked, added, err)
	}
	if _, err := db.ActPoolLease(ctx, leases[0].ID, original.ID, "commit", 0); err != nil {
		t.Fatal(err)
	}
	if linked, added, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, account.ID); err != nil || linked != 1 || added != 1 {
		t.Fatalf("committed lease was not shared: %d %d %v", linked, added, err)
	}
}

func TestPoolSharedPendingReleasePreservesMailboxCredentials(t *testing.T) {
	db, _, _, aliases, original := poolFixture(t, 1, "")
	ctx := context.Background()
	leases, err := db.ClaimPool(ctx, original.ID, store.PoolClaim{RequestID: "legacy-pending-share", Count: 1, TTLSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.GetAlias(ctx, aliases[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	// Cover sharing records created before pending leases were excluded.
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO pool_project_aliases(client_id,alias_id,created_at) VALUES(?,?,?)`, deepseek.ID, before.ID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActPoolLease(ctx, leases[0].ID, original.ID, "release", 0); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetAlias(ctx, before.ID)
	if err != nil || after.CredentialVersion != before.CredentialVersion || after.CredentialCiphertext != before.CredentialCiphertext {
		t.Fatal("closing a pending lease revoked shared credentials", err)
	}
	var memberState string
	if err := db.DB().QueryRowContext(ctx, `SELECT state FROM pool_members WHERE alias_id=?`, before.ID).Scan(&memberState); err != nil || memberState != "paused" {
		t.Fatalf("shared member state=%s err=%v", memberState, err)
	}
}

func TestPoolSharedRetirementResumePreservesOtherProjects(t *testing.T) {
	db, client, lease, operationID, items := retirementReconcileFixture(t)
	ctx := context.Background()
	if _, err := db.DB().ExecContext(ctx, `UPDATE pool_alias_retirement_job_items SET state='retiring' WHERE operation_id=?`, operationID); err != nil {
		t.Fatal(err)
	}
	if err := db.RestorePoolAliasRetirement(ctx, operationID, 0, "APPLE_DELETE_FAILED", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPoolAliasRetirementJobStatus(ctx, operationID, client.ID, store.PoolAliasRetirementJobCompleted); err != nil {
		t.Fatal(err)
	}
	alias, err := db.GetAlias(ctx, lease.AliasID)
	if err != nil {
		t.Fatal(err)
	}
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LinkPoolProjectAccountAliases(ctx, deepseek.ID, alias.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := db.AcquirePoolAliasRetirementReconcile(ctx, client.ID, operationID, items); err != nil || !acquired {
		t.Fatalf("reconciliation acquire=%v err=%v", acquired, err)
	}
	if err := db.PreparePoolAliasRetirementResume(ctx, client.ID, operationID, 0); !errors.Is(err, store.ErrPoolRetirementShared) {
		t.Fatalf("shared alias resumed for Apple deletion: %v", err)
	}
	if err := db.FinalizeReconciledPoolAliasRetirement(ctx, client.ID, operationID, 0); !errors.Is(err, store.ErrPoolRetirementShared) {
		t.Fatalf("shared alias removed by reconciliation: %v", err)
	}
	current, err := db.GetAlias(ctx, lease.AliasID)
	if err != nil || current.CredentialVersion != alias.CredentialVersion || current.CredentialCiphertext != alias.CredentialCiphertext {
		t.Fatal("reconciliation changed a shared mailbox", err)
	}
}

func TestPoolSharedRetirementWorkerCannotRemoveLinkedMailbox(t *testing.T) {
	db, client, lease := usedPoolLease(t)
	ctx := context.Background()
	items := []store.PoolAliasRetirementItemInput{{AliasID: lease.AliasID, LeaseID: lease.ID}}
	operationID := "shared-worker-retirement-0001"
	if _, accepted, err := db.StartPoolAliasRetirement(ctx, client.ID, operationID, "fixture", items); err != nil || !accepted {
		t.Fatalf("retirement start=%v err=%v", accepted, err)
	}
	deepseek, _, err := db.CreatePoolClient(ctx, "DeepSeek")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO pool_project_aliases(client_id,alias_id,created_at) VALUES(?,?,?)`, deepseek.ID, lease.AliasID, time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := db.FinalizePoolAliasRetirement(ctx, operationID, 0, lease.AliasID); !errors.Is(err, store.ErrPoolRetirementShared) {
		t.Fatalf("worker finalized linked mailbox: %v", err)
	}
	if _, err := db.GetAlias(ctx, lease.AliasID); err != nil {
		t.Fatalf("worker removed linked mailbox: %v", err)
	}
}
