package mail

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"icloud-api/internal/domain"
)

func TestFetchAliasIncrementalFirstCallFindsTargetAndOnlyDownloadsMatch(t *testing.T) {
	f := startArchiveIMAPFixture(t, 0, 0)
	unrelated := "From: x@example.test\r\nX-Original-To: other@example.test\r\nSubject: unrelated\r\n\r\nother\r\n"
	if _, err := f.user.Append("INBOX", strings.NewReader(unrelated), &imap.AppendOptions{Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	target := "From: x@example.test\r\nX-Original-To: alias@example.test\r\nSubject: target\r\n\r\ncode\r\n"
	if _, err := f.user.Append("INBOX", strings.NewReader(target), &imap.AppendOptions{Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	result, err := fetcher.FetchAliasIncremental(context.Background(), f.account, "fixture-password", f.alias, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reset || len(result.ArchivedMessages) != 1 {
		t.Fatalf("reset=%v messages=%d", result.Reset, len(result.ArchivedMessages))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodyUIDs) != 1 || f.bodyUIDs[0] != 2 {
		t.Fatalf("body UIDs=%v, want [2]", f.bodyUIDs)
	}
}

func TestFetchAliasIncrementalCursorsAreIndependentAndRespectBatchLimit(t *testing.T) {
	f := startArchiveIMAPFixture(t, 3, 0)
	fetcher := &Fetcher{MaxIncrementalCandidates: 2, ArchiveTempDir: t.TempDir()}
	state := f.initialState(t)
	first, err := fetcher.FetchAliasIncremental(context.Background(), f.account, "fixture-password", f.alias, nil, &state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ArchivedMessages) != 2 || !first.HasMore {
		t.Fatalf("first batch=%d more=%v", len(first.ArchivedMessages), first.HasMore)
	}
	second, err := fetcher.FetchAliasIncremental(context.Background(), f.account, "fixture-password", f.alias, nil, &first.State, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ArchivedMessages) != 1 || second.HasMore {
		t.Fatalf("second batch=%d more=%v", len(second.ArchivedMessages), second.HasMore)
	}
}

func TestFetchAliasIncrementalRejectsInvalidAliasBeforeNetwork(t *testing.T) {
	f := startArchiveIMAPFixture(t, 1, 0)
	bad := f.alias
	bad.AccountID++
	if _, err := NewFetcher().FetchAliasIncremental(context.Background(), f.account, "fixture-password", bad, nil, nil, nil); err != ErrInvalidAlias {
		t.Fatalf("error=%v, want ErrInvalidAlias", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.selections != 0 {
		t.Fatalf("network selections=%d, want 0", f.selections)
	}
}

func TestFetchAliasIncrementalSharedRecipientsPublishOnlyRequestedAlias(t *testing.T) {
	for _, mailboxType := range []string{domain.MailboxTypeCustom, domain.MailboxTypeICloud} {
		t.Run(mailboxType, func(t *testing.T) {
			f := startArchiveIMAPFixture(t, 0, 0)
			f.account.MailboxType = mailboxType
			second := f.alias
			second.ID++
			second.Address = "second@example.test"
			known := []domain.Alias{f.alias, second}
			raw := "From: sender@example.test\r\nX-Original-To: alias@example.test, second@example.test\r\n" +
				"Delivered-To: primary@example.test\r\nTo: alias@example.test, second@example.test\r\n" +
				"Subject: shared\r\n\r\nShared verification code 123456\r\n"
			if _, err := f.user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{Time: time.Now()}); err != nil {
				t.Fatal(err)
			}
			fetcher := &Fetcher{ArchiveTempDir: t.TempDir()}
			for _, target := range known {
				result, err := fetcher.FetchAliasIncremental(context.Background(), f.account, "fixture-password", target, known, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				if !result.Reset || result.HasMore || result.State.LastUID != 1 || len(result.ArchivedMessages) != 1 {
					t.Fatalf("target %d result = %#v", target.ID, result)
				}
				message := result.ArchivedMessages[0]
				if message.UID != 1 || len(message.AliasIDs) != 1 || message.AliasIDs[0] != target.ID {
					t.Fatalf("target %d archived shared recipients = %#v", target.ID, message.AliasIDs)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.bodyUIDs) != 2 || f.bodyUIDs[0] != 1 || f.bodyUIDs[1] != 1 {
				t.Fatalf("shared body UIDs = %v, want [1 1]", f.bodyUIDs)
			}
		})
	}
}

func TestFetchAliasIncrementalSkipsSubstringAndConflictingRecipientsBeforeBody(t *testing.T) {
	for _, test := range []struct {
		name        string
		mailboxType string
		address     string
		headers     string
	}{
		{
			name: "substring", mailboxType: domain.MailboxTypeCustom, address: "notalias@example.test",
			headers: "X-Original-To: notalias@example.test\r\nTo: notalias@example.test\r\n",
		},
		{
			name: "visible_only_target", mailboxType: domain.MailboxTypeCustom, address: "second@example.test",
			headers: "X-Original-To: second@example.test\r\nTo: alias@example.test\r\n",
		},
		{
			name: "icloud_sibling_conflict", mailboxType: domain.MailboxTypeICloud, address: "second@example.test",
			headers: "X-Original-To: alias@example.test\r\nDelivered-To: primary@example.test\r\nTo: second@example.test\r\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := startArchiveIMAPFixture(t, 0, 0)
			f.account.MailboxType = test.mailboxType
			second := f.alias
			second.ID++
			second.Address = test.address
			raw := "From: sender@example.test\r\n" + test.headers + "Subject: other recipient\r\n\r\nDo not fetch this body\r\n"
			if _, err := f.user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{Time: time.Now()}); err != nil {
				t.Fatal(err)
			}
			fetcher := &Fetcher{ArchiveTempDir: t.TempDir()}
			result, err := fetcher.FetchAliasIncremental(context.Background(), f.account, "fixture-password", f.alias,
				[]domain.Alias{f.alias, second}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Reset || result.HasMore || result.State.LastUID != 1 || len(result.ArchivedMessages) != 0 {
				t.Fatalf("non-target result = %#v", result)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.headerFetches == 0 || len(f.bodyUIDs) != 0 {
				t.Fatalf("expected header-only validation, headers=%d body UIDs=%v", f.headerFetches, f.bodyUIDs)
			}
		})
	}
}

func TestFetchAliasIncrementalRequiresMatchingEnabledTargetInKnownAliases(t *testing.T) {
	for _, name := range []string{"missing", "address_changed", "disabled", "account_changed", "other_snapshot"} {
		t.Run(name, func(t *testing.T) {
			f := startArchiveIMAPFixture(t, 0, 0)
			known := []domain.Alias{f.alias}
			var positions map[int64]domain.MailboxSnapshotPosition
			switch name {
			case "missing":
				known = []domain.Alias{}
			case "address_changed":
				known[0].Address = "changed@example.test"
			case "disabled":
				known[0].Enabled = false
			case "account_changed":
				known[0].AccountID++
			case "other_snapshot":
				second := f.alias
				second.ID++
				second.Address = "second@example.test"
				known = append(known, second)
				positions = map[int64]domain.MailboxSnapshotPosition{
					second.ID: {AliasID: second.ID, UIDValidity: 1, UID: 1},
				}
			}
			_, err := NewFetcher().FetchAliasIncremental(context.Background(), f.account, "fixture-password", f.alias, known, nil, positions)
			if err == nil || name != "other_snapshot" && err != ErrInvalidAlias {
				t.Fatalf("invalid known alias context error = %v", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.selections != 0 {
				t.Fatalf("invalid context selected mailbox %d times", f.selections)
			}
		})
	}
}
