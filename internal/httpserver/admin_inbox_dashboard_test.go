package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func TestInboxRecentFiveMessagesSanitizationPlusTagAndPublicShare(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	baseNow := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	now := baseNow
	env.server.now = func() time.Time { return now }
	if err := env.store.ConfigureMailArchive(t.TempDir(), 4<<20); err != nil {
		t.Fatalf("configure mail archive: %v", err)
	}

	cookie, csrf, _ := env.createSession(t, "inbox-admin", "unused-password")
	account := adminAPITestCreateAccount(t, env, "inbox-owner@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "hidebox@icloud.com")
	account, err := env.store.GetAccount(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Archive 7 messages: 5 generic + 2 addressed to +chatgpt, with malicious HTML on the newest one.
	archived := make([]domain.ArchivedMessage, 0, 7)
	for i := 1; i <= 7; i++ {
		msgTime := baseNow.Add(time.Duration(i) * time.Minute)
		toAddr := "hidebox@icloud.com"
		if i == 3 || i == 7 {
			toAddr = "hidebox+chatgpt@icloud.com"
		}
		otp := fmt.Sprintf("60000%d", i)
		htmlBody := fmt.Sprintf(`<p>Hello #%d code <b>%s</b></p>`, i, otp)
		if i == 7 {
			htmlBody = `<script>alert('xss')</script><img src="https://tracker.example.com/pixel.png" onerror="alert(1)"><a href="javascript:alert(2)">bad</a><a href="https://openai.com/verify">Verify</a><p onclick="evil()">Code: <strong>` + otp + `</strong></p>`
		}
		rawMIME := strings.Join([]string{
			"From: Sender <noreply@openai.com>",
			"To: " + toAddr,
			fmt.Sprintf("Subject: Message %d", i),
			fmt.Sprintf("Message-ID: <msg-%d@example.test>", i),
			"MIME-Version: 1.0",
			"Content-Type: text/html; charset=utf-8",
			"",
			htmlBody,
		}, "\r\n")
		archived = append(archived, domain.ArchivedMessage{
			AccountID:    account.ID,
			UIDValidity:  101,
			UID:          uint32(i),
			MessageID:    fmt.Sprintf("<msg-%d@example.test>", i),
			InternalDate: msgTime,
			From:         []domain.MailAddress{{Name: "Sender", Email: "noreply@openai.com"}},
			To:           []domain.MailAddress{{Email: toAddr}},
			Subject:      fmt.Sprintf("Message %d", i),
			RawMIME:      []byte(rawMIME),
			OTP:          otp,
			AliasIDs:     []int64{alias.ID},
		})
	}
	if err := env.store.ApplyMailboxSync(
		context.Background(),
		account.ID,
		account.UpdatedAt,
		[]domain.Alias{alias},
		domain.MailboxSyncResult{
			ArchivedMessages: archived,
			State:            domain.IMAPSyncState{AccountID: account.ID, UIDValidity: 101, LastUID: 7, UpdatedAt: baseNow.Add(7 * time.Minute)},
			Reset:            true,
		},
		baseNow.Add(7*time.Minute),
	); err != nil {
		t.Fatalf("archive messages: %v", err)
	}

	// 1. Lookup with +tag resolves root alias and returns matched_tag.
	lookupResp := env.request(t, http.MethodGet, "/admin/api/v1/inbox/lookup?address="+url.QueryEscape("hidebox+chatgpt@icloud.com"), nil, "", []*http.Cookie{cookie}, "")
	if lookupResp.Code != http.StatusOK {
		t.Fatalf("lookup status = %d body=%s", lookupResp.Code, lookupResp.Body.String())
	}
	var lookupPayload struct {
		Data struct {
			Alias      inboxAliasDTO `json:"alias"`
			MatchedTag string        `json:"matched_tag"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lookupResp.Body.Bytes(), &lookupPayload); err != nil {
		t.Fatal(err)
	}
	if lookupPayload.Data.Alias.ID != alias.ID || lookupPayload.Data.MatchedTag != "chatgpt" || lookupPayload.Data.Alias.InboxSharePath == "" {
		t.Fatalf("unexpected lookup payload: %+v", lookupPayload.Data)
	}

	// 2. Recent 5 messages endpoint returns newest 5 (UIDs 7..3) with XSS & remote tracker stripped.
	msgResp := env.request(t, http.MethodGet, fmt.Sprintf("/admin/api/v1/aliases/%d/messages?limit=5", alias.ID), nil, "", []*http.Cookie{cookie}, "")
	if msgResp.Code != http.StatusOK {
		t.Fatalf("messages status = %d body=%s", msgResp.Code, msgResp.Body.String())
	}
	var msgPayload struct {
		Data struct {
			Alias         inboxAliasDTO     `json:"alias"`
			Messages      []inboxMessageDTO `json:"messages"`
			TotalArchived int               `json:"total_archived"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msgResp.Body.Bytes(), &msgPayload); err != nil {
		t.Fatal(err)
	}
	if msgPayload.Data.TotalArchived != 7 || len(msgPayload.Data.Messages) != 5 {
		t.Fatalf("got %d messages (total %d), want 5 (total 7)", len(msgPayload.Data.Messages), msgPayload.Data.TotalArchived)
	}
	newest := msgPayload.Data.Messages[0]
	if newest.MailboxUID != 7 || newest.OTP != "600007" || !newest.RemoteImagesBlocked {
		t.Fatalf("unexpected newest message: %+v", newest)
	}
	for _, forbidden := range []string{"<script", "onerror", "onclick", "javascript:", "tracker.example.com"} {
		if strings.Contains(strings.ToLower(newest.SafeHTML), forbidden) {
			t.Fatalf("SafeHTML leaked %q: %s", forbidden, newest.SafeHTML)
		}
	}
	if !strings.Contains(newest.SafeHTML, "https://openai.com/verify") || !strings.Contains(newest.SafeHTML, "noopener noreferrer") {
		t.Fatalf("SafeHTML dropped safe link: %s", newest.SafeHTML)
	}

	// 3. Filtering by +chatgpt returns exact 2 tagged messages (UID 7 and UID 3) even though UID 3 was 5th overall.
	tagResp := env.request(t, http.MethodGet, fmt.Sprintf("/admin/api/v1/aliases/%d/messages?limit=5&tag=chatgpt", alias.ID), nil, "", []*http.Cookie{cookie}, "")
	if tagResp.Code != http.StatusOK {
		t.Fatalf("tag filter status = %d", tagResp.Code)
	}
	var tagPayload struct {
		Data struct {
			Messages []inboxMessageDTO `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(tagResp.Body.Bytes(), &tagPayload); err != nil {
		t.Fatal(err)
	}
	if len(tagPayload.Data.Messages) != 2 || tagPayload.Data.Messages[0].MailboxUID != 7 || tagPayload.Data.Messages[1].MailboxUID != 3 {
		t.Fatalf("tag filtered messages = %+v", tagPayload.Data.Messages)
	}

	// 4. Public read-only share page & JSON API work without session cookie, hide admin fields, and revoke on rotation.
	shareURL, err := url.Parse(lookupPayload.Data.Alias.InboxSharePath)
	if err != nil {
		t.Fatal(err)
	}
	shareToken := shareURL.Query().Get("token")
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	pubHTML := serveV2Request(router, http.MethodGet, "/inbox/view?token="+url.QueryEscape(shareToken), "", nil)
	if pubHTML.Code != http.StatusOK || !strings.Contains(pubHTML.Body.String(), "600007") || strings.Contains(pubHTML.Body.String(), "inbox-owner@icloud.com") {
		t.Fatalf("public HTML share failed: code=%d body=%s", pubHTML.Code, pubHTML.Body.String())
	}
	now = now.Add(4 * time.Second)
	pubAPI := serveV2Request(router, http.MethodGet, "/api/v1/inbox?token="+url.QueryEscape(shareToken), "", nil)
	if pubAPI.Code != http.StatusOK || strings.Contains(pubAPI.Body.String(), "inbox-owner@icloud.com") || strings.Contains(pubAPI.Body.String(), "api_key") {
		t.Fatalf("public JSON share failed: code=%d body=%s", pubAPI.Code, pubAPI.Body.String())
	}
	// Cross-purpose check: InboxToken cannot read /api/v1/otp!
	if cross := serveV2Request(router, http.MethodGet, "/api/v1/otp?token="+url.QueryEscape(shareToken), "", nil); cross.Code != http.StatusUnauthorized {
		t.Fatalf("inbox share token accepted on /api/v1/otp: %d", cross.Code)
	}
	// Rotating credentials revokes share token immediately.
	if _, err := env.store.RotateAliasCredentials(context.Background(), alias.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Second)
	if revoked := serveV2Request(router, http.MethodGet, "/api/v1/inbox?token="+url.QueryEscape(shareToken), "", nil); revoked.Code != http.StatusUnauthorized {
		t.Fatalf("rotated inbox share token status = %d, want 401", revoked.Code)
	}

	// 5. Dashboard summary & Archive GC endpoints.
	dashResp := env.request(t, http.MethodGet, "/admin/api/v1/dashboard/summary", nil, "", []*http.Cookie{cookie}, "")
	if dashResp.Code != http.StatusOK || !strings.Contains(dashResp.Body.String(), `"with_mail":1`) {
		t.Fatalf("dashboard summary = %d body=%s", dashResp.Code, dashResp.Body.String())
	}
	gcResp := env.request(t, http.MethodPost, "/admin/api/v1/archive/gc", []byte("{}"), "application/json", []*http.Cookie{cookie}, csrf)
	if gcResp.Code != http.StatusOK || !strings.Contains(gcResp.Body.String(), `"cleaned_temp_files"`) {
		t.Fatalf("archive gc = %d body=%s", gcResp.Code, gcResp.Body.String())
	}
}

func TestMultiProjectSharedMailboxFilterRateIsolationAndIndependentUnlink(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.sync = nil
	now := time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC)
	env.server.now = func() time.Time { return now }
	if err := env.store.ConfigureMailArchive(t.TempDir(), 2<<20); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "multi-proj@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "shared-hme@icloud.com")
	account, _ = env.store.GetAccount(ctx, account.ID)

	// Seed 2 OTP emails on the SAME HME mailbox: one from OpenAI, one from DeepSeek.
	if err := env.store.ApplyMailboxSync(ctx, account.ID, account.UpdatedAt, []domain.Alias{alias}, domain.MailboxSyncResult{
		ArchivedMessages: []domain.ArchivedMessage{
			{
				AccountID: account.ID, UIDValidity: 55, UID: 1, InternalDate: now.Add(-2 * time.Minute),
				From:    []domain.MailAddress{{Name: "OpenAI", Email: "noreply@tm.openai.com"}},
				To:      []domain.MailAddress{{Email: "shared-hme@icloud.com"}},
				Subject: "Your ChatGPT code is 111222", RawMIME: []byte("Subject: ChatGPT\r\n\r\n111222"),
				OTP: "111222", AliasIDs: []int64{alias.ID},
			},
			{
				AccountID: account.ID, UIDValidity: 55, UID: 2, InternalDate: now.Add(-time.Minute),
				From:    []domain.MailAddress{{Name: "DeepSeek", Email: "noreply@deepseek.com"}},
				To:      []domain.MailAddress{{Email: "shared-hme@icloud.com"}},
				Subject: "DeepSeek 验证码 333444", RawMIME: []byte("Subject: DeepSeek\r\n\r\n333444"),
				OTP: "333444", AliasIDs: []int64{alias.ID},
			},
		},
		State: domain.IMAPSyncState{AccountID: account.ID, UIDValidity: 55, LastUID: 2, UpdatedAt: now},
		Reset: true,
	}, now); err != nil {
		t.Fatal(err)
	}

	openaiClient, _, err := env.store.CreatePoolClientWithRules(ctx, "ChatGPT", "openai.com", "")
	if err != nil {
		t.Fatal(err)
	}
	deepseekClient, _, err := env.store.CreatePoolClientWithRules(ctx, "DeepSeek", "deepseek.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.LinkPoolProjectAccountAliases(ctx, openaiClient.ID, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := env.store.LinkPoolProjectAccountAliases(ctx, deepseekClient.ID, account.ID); err != nil {
		t.Fatal(err)
	}

	cookie, csrf, _ := env.createSession(t, "pool-proj-admin", "unused-password")
	getProjectOTPPath := func(clientID string) string {
		r := httptest.NewRequest(http.MethodGet, "http://admin.example.test/admin/api/v1/pool/projects/"+clientID+"/mailboxes?account_id="+strconv.FormatInt(account.ID, 10), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("list project mailboxes: %d %s", w.Code, w.Body.String())
		}
		var payload struct {
			Data struct {
				Items []struct {
					ProjectOTPURLPath string `json:"project_otp_url_path"`
				} `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || len(payload.Data.Items) != 1 {
			t.Fatalf("decode project mailboxes: %v %+v", err, payload)
		}
		return payload.Data.Items[0].ProjectOTPURLPath
	}

	openaiPath := getProjectOTPPath(openaiClient.ID)
	deepseekPath := getProjectOTPPath(deepseekClient.ID)

	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}

	// Both projects poll at the EXACT same instant `now`: neither gets 429, and each gets ONLY its own service's OTP!
	respA := serveV2Request(router, http.MethodGet, openaiPath, "", nil)
	respB := serveV2Request(router, http.MethodGet, deepseekPath, "", nil)
	if respA.Code != http.StatusOK || respB.Code != http.StatusOK {
		t.Fatalf("simultaneous project pickups failed: A=%d (%s) B=%d (%s)", respA.Code, respA.Body.String(), respB.Code, respB.Body.String())
	}
	if !strings.Contains(respA.Body.String(), `"111222"`) || strings.Contains(respA.Body.String(), `"333444"`) {
		t.Fatalf("OpenAI project received wrong OTPs: %s", respA.Body.String())
	}
	if !strings.Contains(respB.Body.String(), `"333444"`) || strings.Contains(respB.Body.String(), `"111222"`) {
		t.Fatalf("DeepSeek project received wrong OTPs: %s", respB.Body.String())
	}

	// Unlinking DeepSeek revokes DeepSeek's token immediately while OpenAI's token keeps working!
	unlinkReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("http://admin.example.test/admin/api/v1/pool/projects/%s/mailboxes/%d", deepseekClient.ID, alias.ID), nil)
	unlinkReq.Header.Set("Origin", "http://admin.example.test")
	unlinkReq.Header.Set("X-CSRF-Token", csrf)
	unlinkReq.AddCookie(cookie)
	unlinkResp := httptest.NewRecorder()
	env.router.ServeHTTP(unlinkResp, unlinkReq)
	if unlinkResp.Code != http.StatusOK {
		t.Fatalf("unlink deepseek: %d %s", unlinkResp.Code, unlinkResp.Body.String())
	}

	now = now.Add(3 * time.Second)
	if revoked := serveV2Request(router, http.MethodGet, deepseekPath, "", nil); revoked.Code != http.StatusUnauthorized {
		t.Fatalf("unlinked project token status = %d, want 401", revoked.Code)
	}
	if stillValid := serveV2Request(router, http.MethodGet, openaiPath, "", nil); stillValid.Code != http.StatusOK || !strings.Contains(stillValid.Body.String(), `"111222"`) {
		t.Fatalf("other project broke after unlink: %d %s", stillValid.Code, stillValid.Body.String())
	}
}

func TestAutoEnrollSkipsDirectoryImportsAndEnrollsNewAliasesImmediately(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	account := adminAPITestCreateAccount(t, env, "auto-enroll-fix@icloud.com")
	if err := env.store.SetPoolAccount(ctx, account.ID, true, 10); err != nil {
		t.Fatal(err)
	}

	// 1. Directory import must NOT auto-enroll pre-existing external addresses even when auto_enroll is ON.
	_, _, err := env.store.ImportAliasesWithCredentials(ctx, account.ID, []domain.AliasImportCandidate{
		{Address: "historical-imported@icloud.com", Label: "old", Active: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.RefreshPool(ctx); err != nil {
		t.Fatal(err)
	}
	members, err := env.store.ListPoolMembers(ctx, "", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if members.Total != 0 {
		t.Fatalf("imported historical alias unexpectedly auto-enrolled: %+v", members.Items)
	}

	// 2. Newly created alias auto-enrolls IMMEDIATELY inside CreateAlias without waiting for RefreshPool!
	created, err := env.store.CreateAlias(ctx, domain.Alias{
		AccountID: account.ID,
		Address:   "freshly-created@icloud.com",
		Enabled:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	members, err = env.store.ListPoolMembers(ctx, "available", "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if members.Total != 1 || members.Items[0].AliasID != created.ID {
		t.Fatalf("newly created alias not immediately enrolled: %+v", members)
	}

	// 3. EnrollPoolAliases is idempotent if an already-available alias is submitted again in a batch,
	// while pool_enrollable=true only returns unenrolled eligible aliases (the imported one!).
	enrollablePage, err := env.store.ListAliasesPage(ctx, store.AliasListFilter{
		AccountID:           &account.ID,
		OnlyEnabledAccounts: true,
		Enabled:             new(true),
		PoolEnrollable:      true,
		Limit:               50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if enrollablePage.Total != 1 || enrollablePage.Items[0].Address != "historical-imported@icloud.com" {
		t.Fatalf("PoolEnrollable filter = %+v", enrollablePage)
	}
	if err := env.store.EnrollPoolAliases(ctx, []int64{created.ID, enrollablePage.Items[0].ID}); err != nil {
		t.Fatalf("idempotent EnrollPoolAliases failed: %v", err)
	}
}
