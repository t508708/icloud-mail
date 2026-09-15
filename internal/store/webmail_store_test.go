package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"icloud-api/internal/store"
)

func TestAliasWebMailboxSyncDoesNotAdvanceIMAPCursor(t *testing.T) {
	db, account, alias, _ := aliasIMAPSyncFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 13, 2, 0, 0, 0, time.UTC)
	applyAliasIMAPSyncBatch(t, ctx, db, alias, aliasIMAPSyncResult(alias, 91, 9, at), at)
	before, err := db.GetAliasIMAPSyncState(ctx, alias.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	result := aliasIMAPSyncResult(alias, 91, 120, at.Add(time.Minute))
	if err := db.ApplyAliasWebMailboxSync(ctx, current.UpdatedAt, alias, result, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetAliasIMAPSyncState(ctx, alias.ID)
	if err != nil || after != before {
		t.Fatalf("webmail changed IMAP cursor: before=%#v after=%#v err=%v", before, after, err)
	}
	if _, err := db.GetLatestMessage(ctx, alias.ID); errors.Is(err, store.ErrNotFound) {
		t.Fatal("webmail did not publish alias projection")
	}
}

func TestWebMailArchiveKeepsExistingMIMEAndOtherAlias(t *testing.T) {
	db, account, alias, other := aliasIMAPSyncFixture(t)
	ctx := context.Background()
	at := time.Now().UTC()
	r := aliasIMAPSyncResult(alias, 91, 9, at)
	raw := []byte("Subject: original\r\nContent-Type: text/plain\r\n\r\nComplete MIME with attachments fixture")
	r.ArchivedMessages[0].RawMIME = raw
	applyAliasIMAPSyncBatch(t, ctx, db, alias, r, at)
	initial, err := db.ListArchivedMailboxMessages(ctx, alias.ID)
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial archive: %v", err)
	}
	untouched, err := db.GetAlias(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := db.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.ArchivedMessages[0].RawMIME = []byte("reconstructed text only")
	r.ArchivedMessages[0].BodyTruncated = true
	if err := db.ApplyAliasWebMailboxSync(ctx, current.UpdatedAt, alias, r, at); err != nil {
		t.Fatal(err)
	}
	after, err := db.ListArchivedMailboxMessages(ctx, alias.ID)
	if err != nil || len(after) != 1 || after[0].ContentSHA256 != initial[0].ContentSHA256 || after[0].BodyTruncated || after[0].MailboxUID != initial[0].MailboxUID {
		t.Fatalf("existing archive replaced: %#v %v", after, err)
	}
	file, err := db.OpenArchivedContent(after[0])
	if err != nil {
		t.Fatal(err)
	}
	savedRaw, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(savedRaw, raw) {
		t.Fatal("original MIME file was overwritten")
	}
	currentOther, err := db.GetAlias(ctx, other.ID)
	if err != nil || !reflect.DeepEqual(untouched, currentOther) {
		t.Fatal("other alias modified")
	}
	if !bytes.Equal(r.ArchivedMessages[0].RawMIME, []byte("reconstructed text only")) {
		t.Fatal("caller-owned result mutated")
	}
}

func TestWebMailArchiveRechecksAccountVersionAndEnabledState(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			db, account, alias, _ := aliasIMAPSyncFixture(t)
			ctx := context.Background()
			at := time.Now().UTC()
			if _, err := db.DB().ExecContext(ctx, `UPDATE accounts SET enabled = ?, updated_at = updated_at + 1 WHERE id = ?`, !disabled, account.ID); err != nil {
				t.Fatal(err)
			}
			result := aliasIMAPSyncResult(alias, 91, 9, at)
			if err := db.ApplyAliasWebMailboxSync(ctx, account.UpdatedAt, alias, result, at); !errors.Is(err, store.ErrAliasMailboxSyncStale) {
				t.Fatalf("stale publication: %v", err)
			}
			if rows, err := db.ListArchivedMailboxMessages(ctx, alias.ID); err != nil || len(rows) != 0 {
				t.Fatal("stale archive committed")
			}
		})
	}
}
