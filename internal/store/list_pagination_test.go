package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestListAccountsPageOrdersWithoutOverlapAndReportsTotal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	for _, fixture := range []struct {
		name  string
		email string
	}{
		{name: "Echo", email: "echo-page@icloud.com"},
		{name: "Alpha", email: "alpha-page@icloud.com"},
		{name: "Delta", email: "delta-page@icloud.com"},
		{name: "Bravo", email: "bravo-page@icloud.com"},
		{name: "Charlie", email: "charlie-page@icloud.com"},
	} {
		createAccount(t, ctx, db, fixture.name, fixture.email)
	}

	var got []string
	for offset := 0; offset < 6; offset += 2 {
		page, err := db.ListAccountsPage(ctx, store.AccountListFilter{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("list account page at offset %d: %v", offset, err)
		}
		if page.Total != 5 {
			t.Fatalf("account page total at offset %d = %d, want 5", offset, page.Total)
		}
		if len(page.Items) > 2 {
			t.Fatalf("account page at offset %d returned %d items, want at most 2", offset, len(page.Items))
		}
		for _, account := range page.Items {
			got = append(got, account.Email)
		}
	}

	want := []string{
		"alpha-page@icloud.com",
		"bravo-page@icloud.com",
		"charlie-page@icloud.com",
		"delta-page@icloud.com",
		"echo-page@icloud.com",
	}
	assertStringsEqual(t, got, want)
}

func TestListAccountsPageFiltersEmailAndNameCaseInsensitively(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	createAccount(t, ctx, db, "Production Team", "owner@icloud.com")
	createAccount(t, ctx, db, "Sales", "billing-special@icloud.com")
	createAccount(t, ctx, db, "100% Service", "percent@icloud.com")
	createAccount(t, ctx, db, "Unrelated", "other@icloud.com")
	custom, err := db.CreateAccount(ctx, domain.Account{
		Name:               "Custom suffix owner",
		Email:              "custom-search@identity.invalid",
		MailboxType:        domain.MailboxTypeCustom,
		EmailSuffix:        "Tenant.Custom.Test",
		IMAPHost:           "imap.custom.test",
		IMAPPort:           993,
		IMAPUsername:       "shared-login",
		PasswordCiphertext: "encrypted",
		Enabled:            true,
	})
	if err != nil {
		t.Fatalf("create custom searchable account: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "name", query: "PRODUCTION", want: "owner@icloud.com"},
		{name: "email", query: "SPECIAL", want: "billing-special@icloud.com"},
		{name: "literal wildcard", query: "%", want: "percent@icloud.com"},
		{name: "custom suffix", query: "CUSTOM.TEST", want: custom.Email},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := db.ListAccountsPage(ctx, store.AccountListFilter{
				Query: test.query,
				Limit: 10,
			})
			if err != nil {
				t.Fatalf("search accounts for %q: %v", test.query, err)
			}
			if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Email != test.want {
				t.Fatalf("search accounts for %q = total %d items %#v, want %q", test.query, page.Total, page.Items, test.want)
			}
		})
	}
}

func TestListAliasesPageOrdersFiltersAndReportsTotal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	first := createAccount(t, ctx, db, "First", "first-alias-page@icloud.com")
	second := createAccount(t, ctx, db, "Second", "second-alias-page@icloud.com")

	for index, fixture := range []struct {
		accountID int64
		address   string
	}{
		{accountID: first.ID, address: "echo-alias-page@icloud.com"},
		{accountID: first.ID, address: "alpha-alias-page@icloud.com"},
		{accountID: second.ID, address: "foxtrot-alias-page@icloud.com"},
		{accountID: first.ID, address: "charlie-alias-page@icloud.com"},
		{accountID: second.ID, address: "bravo-alias-page@icloud.com"},
		{accountID: first.ID, address: "delta-alias-page@icloud.com"},
	} {
		createAlias(t, ctx, db, fixture.accountID, fixture.address, []byte(fmt.Sprintf("alias-page-hash-%d", index)))
	}

	var all []string
	for offset := 0; offset < 8; offset += 2 {
		page, err := db.ListAliasesPage(ctx, store.AliasListFilter{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("list alias page at offset %d: %v", offset, err)
		}
		if page.Total != 6 {
			t.Fatalf("alias page total at offset %d = %d, want 6", offset, page.Total)
		}
		if len(page.Items) > 2 {
			t.Fatalf("alias page at offset %d returned %d items, want at most 2", offset, len(page.Items))
		}
		for _, alias := range page.Items {
			all = append(all, alias.Address)
		}
	}
	assertStringsEqual(t, all, []string{
		"alpha-alias-page@icloud.com",
		"bravo-alias-page@icloud.com",
		"charlie-alias-page@icloud.com",
		"delta-alias-page@icloud.com",
		"echo-alias-page@icloud.com",
		"foxtrot-alias-page@icloud.com",
	})

	filtered, err := db.ListAliasesPage(ctx, store.AliasListFilter{
		AccountID: &first.ID,
		Limit:     2,
		Offset:    1,
	})
	if err != nil {
		t.Fatalf("list filtered alias page: %v", err)
	}
	if filtered.Total != 4 {
		t.Fatalf("filtered alias total = %d, want 4", filtered.Total)
	}
	if len(filtered.Items) != 2 {
		t.Fatalf("filtered alias item count = %d, want 2", len(filtered.Items))
	}
	for _, alias := range filtered.Items {
		if alias.AccountID != first.ID {
			t.Fatalf("filtered alias belongs to account %d, want %d", alias.AccountID, first.ID)
		}
	}
	assertStringsEqual(t, aliasAddresses(filtered.Items), []string{
		"charlie-alias-page@icloud.com",
		"delta-alias-page@icloud.com",
	})
}

func TestListAliasesPageFiltersEnabledBeforePaginationAndKeepsAccountIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTestStore(t)
	first := createAccount(t, ctx, db, "Enabled first", "enabled-first@icloud.com")
	second := createAccount(t, ctx, db, "Enabled second", "enabled-second@icloud.com")
	for i, fixture := range []struct {
		accountID int64
		address   string
		enabled   bool
	}{{first.ID, "a-disabled@icloud.com", false}, {first.ID, "b-enabled@icloud.com", true}, {second.ID, "c-disabled@icloud.com", false}} {
		_, err := db.CreateAlias(ctx, domain.Alias{AccountID: fixture.accountID, Address: fixture.address, APIKeyHash: []byte(fmt.Sprintf("enabled-filter-%d", i)), Enabled: fixture.enabled})
		if err != nil {
			t.Fatalf("create alias: %v", err)
		}
	}
	for _, want := range []bool{false, true} {
		page, err := db.ListAliasesPage(ctx, store.AliasListFilter{Enabled: &want, AccountID: &first.ID, Limit: 1})
		if err != nil {
			t.Fatalf("filter enabled=%v: %v", want, err)
		}
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].AccountID != first.ID || page.Items[0].Enabled != want {
			t.Fatalf("filter enabled=%v page = total %d items %#v", want, page.Total, page.Items)
		}
	}
	all, err := db.ListAliasesPage(ctx, store.AliasListFilter{Limit: 2, Offset: 1})
	if err != nil || all.Total != 3 || len(all.Items) != 2 {
		t.Fatalf("unset enabled page = %#v, err=%v", all, err)
	}
}

func TestListAliasesPageSearchesAddressAndLabelBeforePagination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	first := createAccount(t, ctx, db, "First", "first-alias-search@icloud.com")
	second := createAccount(t, ctx, db, "Second", "second-alias-search@icloud.com")

	fixtures := []struct {
		accountID int64
		address   string
		label     string
	}{
		{accountID: first.ID, address: "alpha-search@icloud.com", label: "Production Checkout"},
		{accountID: first.ID, address: "literal-percent%@icloud.com", label: "Percent address"},
		{accountID: first.ID, address: "literal_under@icloud.com", label: "Underscore address"},
		{accountID: second.ID, address: "bravo-search@icloud.com", label: "Production Reports"},
		{accountID: second.ID, address: "unrelated-search@icloud.com", label: "Personal"},
	}
	for index, fixture := range fixtures {
		if _, err := db.CreateAlias(ctx, domain.Alias{
			AccountID:  fixture.accountID,
			Address:    fixture.address,
			Label:      fixture.label,
			APIKeyHash: []byte(fmt.Sprintf("alias-search-hash-%d", index)),
			Enabled:    true,
		}); err != nil {
			t.Fatalf("create searchable alias %q: %v", fixture.address, err)
		}
	}

	tests := []struct {
		name      string
		query     string
		accountID *int64
		offset    int
		wantTotal int
		want      []string
	}{
		{name: "address substring ignores case", query: "ALPHA-SEARCH", wantTotal: 1, want: []string{"alpha-search@icloud.com"}},
		{name: "label substring ignores case", query: "PRODUCTION", wantTotal: 2, want: []string{"alpha-search@icloud.com", "bravo-search@icloud.com"}},
		{name: "percent is literal", query: "%", wantTotal: 1, want: []string{"literal-percent%@icloud.com"}},
		{name: "underscore is literal", query: "_", wantTotal: 1, want: []string{"literal_under@icloud.com"}},
		{name: "account and query intersect", query: "production", accountID: &first.ID, wantTotal: 1, want: []string{"alpha-search@icloud.com"}},
		{name: "filter runs before offset", query: "production", offset: 1, wantTotal: 2, want: []string{"bravo-search@icloud.com"}},
		{name: "blank query is ignored", query: "  ", wantTotal: len(fixtures), want: []string{
			"alpha-search@icloud.com",
			"bravo-search@icloud.com",
			"literal-percent%@icloud.com",
			"literal_under@icloud.com",
			"unrelated-search@icloud.com",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := db.ListAliasesPage(ctx, store.AliasListFilter{
				AccountID: test.accountID,
				Query:     test.query,
				Limit:     10,
				Offset:    test.offset,
			})
			if err != nil {
				t.Fatalf("search aliases for %q: %v", test.query, err)
			}
			if page.Total != test.wantTotal {
				t.Fatalf("search aliases for %q total = %d, want %d", test.query, page.Total, test.wantTotal)
			}
			assertStringsEqual(t, aliasAddresses(page.Items), test.want)
		})
	}
}

func TestListAliasesPageFiltersWithoutLatestMailBeforePagination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	first := createAccount(t, ctx, db, "First", "first-without-mail@icloud.com")
	second := createAccount(t, ctx, db, "Second", "second-without-mail@icloud.com")

	fixtures := []struct {
		accountID int64
		address   string
		label     string
	}{
		{accountID: first.ID, address: "alpha-empty-filter@icloud.com", label: "Campaign target"},
		{accountID: first.ID, address: "bravo-with-mail-filter@icloud.com", label: "Campaign target"},
		{accountID: first.ID, address: "charlie-empty-filter@icloud.com", label: "Campaign target"},
		{accountID: first.ID, address: "delta-empty-filter@icloud.com", label: "Personal"},
		{accountID: second.ID, address: "echo-empty-filter@icloud.com", label: "Campaign target"},
	}
	aliases := make(map[string]domain.Alias, len(fixtures))
	for index, fixture := range fixtures {
		alias, err := db.CreateAlias(ctx, domain.Alias{
			AccountID:  fixture.accountID,
			Address:    fixture.address,
			Label:      fixture.label,
			APIKeyHash: []byte(fmt.Sprintf("without-mail-hash-%d", index)),
			Enabled:    true,
		})
		if err != nil {
			t.Fatalf("create without-mail fixture %q: %v", fixture.address, err)
		}
		aliases[fixture.address] = alias
	}
	seedAliasArchivedMail(t, ctx, db, aliases["bravo-with-mail-filter@icloud.com"], 1,
		time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC))

	filtered, err := db.ListAliasesPage(ctx, store.AliasListFilter{
		AccountID:         &first.ID,
		WithoutLatestMail: true,
		Query:             "campaign",
		Limit:             1,
		Offset:            1,
	})
	if err != nil {
		t.Fatalf("list aliases without latest mail: %v", err)
	}
	if filtered.Total != 2 {
		t.Fatalf("aliases without latest mail total = %d, want 2", filtered.Total)
	}
	if got := aliasAddresses(filtered.Items); len(got) != 1 || got[0] != "charlie-empty-filter@icloud.com" {
		t.Fatalf("aliases without latest mail page = %#v, want charlie fixture", got)
	}
	if filtered.Items[0].LatestReceivedAt != nil {
		t.Fatalf("alias without latest mail has latest timestamp %v", filtered.Items[0].LatestReceivedAt)
	}

	unfiltered, err := db.ListAliasesPage(ctx, store.AliasListFilter{
		AccountID: &first.ID,
		Query:     "campaign",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list aliases with without-mail filter disabled: %v", err)
	}
	if unfiltered.Total != 3 {
		t.Fatalf("unfiltered alias total = %d, want 3", unfiltered.Total)
	}
	assertStringsEqual(t, aliasAddresses(unfiltered.Items), []string{
		"alpha-empty-filter@icloud.com",
		"bravo-with-mail-filter@icloud.com",
		"charlie-empty-filter@icloud.com",
	})
	if unfiltered.Items[1].LatestReceivedAt == nil {
		t.Fatal("archived-mail fixture has nil latest timestamp")
	}
}

func TestListAliasesPageFiltersWithLatestMailBeforePagination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	first := createAccount(t, ctx, db, "First", "first-with-mail@icloud.com")
	second := createAccount(t, ctx, db, "Second", "second-with-mail@icloud.com")
	group, err := db.CreateMailGroup(ctx, "Campaign")
	if err != nil {
		t.Fatalf("create with-mail group: %v", err)
	}
	fixtures := []struct {
		accountID int64
		address   string
		label     string
		groupID   *int64
		hasMail   bool
	}{
		{first.ID, "alpha-empty@icloud.com", "Campaign", &group.ID, false},
		{first.ID, "bravo-with-mail@icloud.com", "Campaign", &group.ID, true},
		{first.ID, "charlie-with-mail@icloud.com", "Campaign", &group.ID, true},
		{first.ID, "delta-with-mail@icloud.com", "Personal", &group.ID, true},
		{first.ID, "echo-with-mail@icloud.com", "Campaign", nil, true},
		{second.ID, "foxtrot-with-mail@icloud.com", "Campaign", &group.ID, true},
	}
	for index, fixture := range fixtures {
		alias, err := db.CreateAlias(ctx, domain.Alias{
			AccountID:  fixture.accountID,
			Address:    fixture.address,
			Label:      fixture.label,
			GroupID:    fixture.groupID,
			APIKeyHash: []byte(fmt.Sprintf("with-mail-hash-%d", index)),
			Enabled:    true,
		})
		if err != nil {
			t.Fatalf("create with-mail fixture %q: %v", fixture.address, err)
		}
		if fixture.hasMail {
			seedAliasArchivedMail(t, ctx, db, alias, uint32(index+1),
				time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC))
		}
	}

	tests := []struct {
		name      string
		filter    store.AliasListFilter
		wantTotal int
		want      []string
	}{
		{
			name:      "all with mail before pagination",
			filter:    store.AliasListFilter{WithLatestMail: true, Limit: 2, Offset: 1},
			wantTotal: 5,
			want:      []string{"charlie-with-mail@icloud.com", "delta-with-mail@icloud.com"},
		},
		{
			name: "account group and query intersect before pagination",
			filter: store.AliasListFilter{
				WithLatestMail: true, AccountID: &first.ID, GroupID: &group.ID,
				Query: "CAMPAIGN", Limit: 1, Offset: 1,
			},
			wantTotal: 2,
			want:      []string{"charlie-with-mail@icloud.com"},
		},
		{
			name: "ungrouped with mail",
			filter: store.AliasListFilter{
				WithLatestMail: true, Ungrouped: true, Limit: 10,
			},
			wantTotal: 1,
			want:      []string{"echo-with-mail@icloud.com"},
		},
		{
			name:      "offset beyond filtered total",
			filter:    store.AliasListFilter{WithLatestMail: true, Limit: 2, Offset: 5},
			wantTotal: 5,
		},
		{
			name: "false leaves mail presence unfiltered",
			filter: store.AliasListFilter{
				WithLatestMail: false, AccountID: &first.ID, GroupID: &group.ID,
				Query: "campaign", Limit: 10,
			},
			wantTotal: 3,
			want: []string{
				"alpha-empty@icloud.com", "bravo-with-mail@icloud.com", "charlie-with-mail@icloud.com",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := db.ListAliasesPage(ctx, test.filter)
			if err != nil {
				t.Fatalf("list aliases with latest mail: %v", err)
			}
			if page.Total != test.wantTotal {
				t.Fatalf("with-mail total = %d, want %d", page.Total, test.wantTotal)
			}
			assertStringsEqual(t, aliasAddresses(page.Items), test.want)
			if test.filter.WithLatestMail {
				for _, alias := range page.Items {
					if alias.LatestReceivedAt == nil {
						t.Fatalf("with-mail alias %q has nil latest timestamp", alias.Address)
					}
				}
			}
		})
	}
}

func TestListAliasesPageRejectsConflictingLatestMailFilters(t *testing.T) {
	t.Parallel()

	// Validation must run before any database access.
	db := &store.Store{}
	if _, err := db.ListAliasesPage(context.Background(), store.AliasListFilter{
		WithLatestMail: true, WithoutLatestMail: true, Limit: 10,
	}); err == nil {
		t.Fatal("alias page accepted conflicting latest-mail filters")
	}
}

func TestListPagesRejectInvalidBounds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openTestStore(t)
	if _, err := db.ListAccountsPage(ctx, store.AccountListFilter{Limit: 0}); err == nil {
		t.Fatal("account page accepted a non-positive limit")
	}
	if _, err := db.ListAccountsPage(ctx, store.AccountListFilter{Limit: 1, Offset: -1}); err == nil {
		t.Fatal("account page accepted a negative offset")
	}
	if _, err := db.ListAccountsPage(ctx, store.AccountListFilter{Limit: 1001}); err == nil {
		t.Fatal("account page accepted a limit above the bounded page size")
	}
	invalidAccountID := int64(0)
	if _, err := db.ListAliasesPage(ctx, store.AliasListFilter{
		AccountID: &invalidAccountID,
		Limit:     1,
	}); err == nil {
		t.Fatal("alias page accepted a non-positive account ID")
	}
	if _, err := db.ListAliasesPage(ctx, store.AliasListFilter{Limit: 1001}); err == nil {
		t.Fatal("alias page accepted a limit above the bounded page size")
	}
}

func aliasAddresses(aliases []domain.Alias) []string {
	addresses := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		addresses = append(addresses, alias.Address)
	}
	return addresses
}

func assertStringsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("values = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("value %d = %q, want %q; all values = %#v", index, got[index], want[index], got)
		}
	}
}

func seedAliasArchivedMail(
	t *testing.T,
	ctx context.Context,
	db *store.Store,
	alias domain.Alias,
	upstreamUID uint32,
	receivedAt time.Time,
) {
	t.Helper()
	timestamp := receivedAt.UTC().UnixNano()
	var messageID int64
	if err := db.DB().QueryRowContext(ctx, `
		INSERT INTO archived_messages(
			account_id, uid_validity, upstream_uid, message_id,
			internal_date, synced_at, created_at
		) VALUES(?, 1, ?, ?, ?, ?, ?)
		RETURNING id`,
		alias.AccountID, int64(upstreamUID), fmt.Sprintf("<filter-%d@example.test>", upstreamUID),
		timestamp, timestamp, timestamp,
	).Scan(&messageID); err != nil {
		t.Fatalf("seed archived message for alias %d: %v", alias.ID, err)
	}
	if _, err := db.DB().ExecContext(ctx, `
		INSERT INTO alias_messages(alias_id, message_id, mailbox_uid, created_at)
		VALUES(?, ?, 1, ?)`, alias.ID, messageID, timestamp); err != nil {
		t.Fatalf("link archived message to alias %d: %v", alias.ID, err)
	}
}
