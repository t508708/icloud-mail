package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/domain"
	"icloud-api/internal/mail"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
	"icloud-api/internal/syncer"
)

const (
	defaultInboxMessageLimit = 5
	maxInboxMessageLimit     = 10
	inboxPreviewBodyBudget   = 512 << 10
	inboxSnippetMaxRunes     = 160
)

type inboxAliasDTO struct {
	ID               int64   `json:"id"`
	AccountID        int64   `json:"account_id,omitempty"`
	AccountEmail     string  `json:"account_email,omitempty"`
	Address          string  `json:"address"`
	Label            string  `json:"label,omitempty"`
	GroupID          *int64  `json:"group_id,omitempty"`
	GroupName        string  `json:"group_name,omitempty"`
	Enabled          bool    `json:"enabled"`
	AccountEnabled   bool    `json:"account_enabled"`
	MailTransport    string  `json:"mail_transport,omitempty"`
	LastSyncedAt     *string `json:"last_synced_at"`
	LatestReceivedAt *string `json:"latest_received_at,omitempty"`
	OTPURLPath       string  `json:"otp_url_path,omitempty"`
	InboxSharePath   string  `json:"inbox_share_path,omitempty"`
}

type inboxSuggestItemDTO struct {
	ID               int64   `json:"id"`
	AccountID        int64   `json:"account_id"`
	AccountEmail     string  `json:"account_email"`
	Address          string  `json:"address"`
	Label            string  `json:"label"`
	GroupName        string  `json:"group_name"`
	Enabled          bool    `json:"enabled"`
	LatestReceivedAt *string `json:"latest_received_at"`
}

type inboxMessageDTO struct {
	ID                  int64                `json:"id"`
	MailboxUID          uint32               `json:"mailbox_uid"`
	MessageID           string               `json:"message_id"`
	Subject             string               `json:"subject"`
	From                []domain.MailAddress `json:"from"`
	To                  []domain.MailAddress `json:"to"`
	CC                  []domain.MailAddress `json:"cc"`
	SentAt              *string              `json:"sent_at"`
	ReceivedAt          string               `json:"received_at"`
	OTP                 string               `json:"otp,omitempty"`
	Snippet             string               `json:"snippet"`
	TextBody            string               `json:"text_body"`
	SafeHTML            string               `json:"safe_html"`
	HasHTML             bool                 `json:"has_html"`
	RemoteImagesBlocked bool                 `json:"remote_images_blocked"`
	ContentState        string               `json:"content_state"`
	ContentBytes        int64                `json:"content_bytes"`
	BodyTruncated       bool                 `json:"body_truncated"`
	Attachments         []domain.Attachment  `json:"attachments"`
	PlusTags            []string             `json:"plus_tags"`
}

func (s *Server) adminAPIDashboardSummary(c *gin.Context) {
	now := s.now().UTC()
	recentSince := now.Add(-time.Hour)
	loc := s.cfg.Timezone
	if loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	todayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).UTC()

	summary, err := s.store.GetDashboardSummary(c.Request.Context(), now, todayStart, recentSince)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	writeAdminAPIData(c, http.StatusOK, summary)
}

func (s *Server) adminAPIRunArchiveGC(c *gin.Context) {
	stats, cleanedTemp, cleanedSessions, err := s.store.RunArchiveMaintenance(c.Request.Context())
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	session := mustSession(c)
	s.audit(
		c,
		&session.AdminID,
		session.Username,
		"archive_gc",
		"archive",
		"",
		"success",
		fmt.Sprintf("temp=%d sessions=%d", cleanedTemp, cleanedSessions),
	)
	writeAdminAPIData(c, http.StatusOK, gin.H{
		"archive": store.DashboardArchiveStats{
			ContentBytes: stats.ContentBytes,
			ContentLimit: stats.ContentLimit,
			MessageCount: stats.MessageCount,
			EvictedCount: stats.EvictedCount,
		},
		"cleaned_temp_files": cleanedTemp,
		"cleaned_sessions":   cleanedSessions,
	})
}

func (s *Server) adminAPIInboxLookup(c *gin.Context) {
	rawAddress := strings.TrimSpace(c.Query("address"))
	if rawAddress == "" || utf8.RuneCountInString(rawAddress) > 320 {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "请输入有效的隐藏邮箱地址")
		return
	}
	alias, matchedTag, err := s.store.LookupAliasForInbox(c.Request.Context(), rawAddress)
	if errors.Is(err, store.ErrNotFound) || adminAPIAliasConfirmationPending(alias) {
		writeAdminAPIError(c, http.StatusNotFound, "NOT_FOUND", "未找到对应的隐藏邮箱")
		return
	}
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	aliasDTO := s.buildInboxAliasDTO(c.Request.Context(), alias, true)
	writeAdminAPIData(c, http.StatusOK, gin.H{
		"alias":       aliasDTO,
		"matched_tag": matchedTag,
	})
}

func (s *Server) adminAPIInboxSuggest(c *gin.Context) {
	limit, ok := adminAPIQueryInt(c, "limit", 12, 1, 30)
	if !ok {
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if utf8.RuneCountInString(query) > 200 {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "检索词长度超出范围")
		return
	}
	aliases, err := s.store.SuggestInboxAliases(c.Request.Context(), query, limit)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	items := make([]inboxSuggestItemDTO, 0, len(aliases))
	for _, alias := range aliases {
		items = append(items, inboxSuggestItemDTO{
			ID:               alias.ID,
			AccountID:        alias.AccountID,
			AccountEmail:     alias.AccountEmail,
			Address:          alias.Address,
			Label:            alias.Label,
			GroupName:        alias.GroupName,
			Enabled:          alias.Enabled && !alias.AccountDisabled,
			LatestReceivedAt: adminAPIOptionalTime(alias.LatestReceivedAt),
		})
	}
	writeAdminAPIData(c, http.StatusOK, gin.H{"items": items})
}

func (s *Server) adminAPIAliasMessages(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	limit, ok := adminAPIQueryInt(c, "limit", defaultInboxMessageLimit, 1, maxInboxMessageLimit)
	if !ok {
		return
	}
	allowRemoteImages := parseTruthyParam(c.Query("remote_images"))
	tag := strings.TrimPrefix(strings.TrimSpace(c.Query("tag")), "+")
	if utf8.RuneCountInString(tag) > 128 {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "子地址标签长度超出范围")
		return
	}

	alias, err := s.store.GetAlias(c.Request.Context(), id)
	if err != nil {
		s.writeAdminAPIStoreReadError(c, err)
		return
	}
	if adminAPIAliasConfirmationPending(alias) {
		writeAdminAPIAliasConfirmationPending(c)
		return
	}

	messages, totalArchived, err := s.loadInboxPreviewMessages(c.Request.Context(), alias, limit, tag, allowRemoteImages)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	writeAdminAPIData(c, http.StatusOK, gin.H{
		"alias":          s.buildInboxAliasDTO(c.Request.Context(), alias, true),
		"messages":       messages,
		"total_archived": totalArchived,
		"sync_warning":   "",
	})
}

func (s *Server) adminAPISyncAliasMessages(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var input struct {
		Limit        int    `json:"limit"`
		Tag          string `json:"tag"`
		RemoteImages bool   `json:"remote_images"`
	}
	if c.Request.ContentLength != 0 {
		if !decodeAdminAPIJSON(c, &input) {
			return
		}
	}
	limit := input.Limit
	if limit < 1 {
		limit = defaultInboxMessageLimit
	}
	if limit > maxInboxMessageLimit {
		limit = maxInboxMessageLimit
	}
	tag := strings.TrimPrefix(strings.TrimSpace(input.Tag), "+")
	if utf8.RuneCountInString(tag) > 128 {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "子地址标签长度超出范围")
		return
	}

	alias, err := s.store.GetAlias(c.Request.Context(), id)
	if err != nil {
		s.writeAdminAPIStoreReadError(c, err)
		return
	}
	if adminAPIAliasConfirmationPending(alias) {
		writeAdminAPIAliasConfirmationPending(c)
		return
	}

	syncWarning := s.triggerOnDemandInboxSync(c.Request.Context(), alias, 15*time.Second)
	if refreshed, refreshErr := s.store.GetAlias(c.Request.Context(), id); refreshErr == nil {
		alias = refreshed
	}

	messages, totalArchived, err := s.loadInboxPreviewMessages(c.Request.Context(), alias, limit, tag, input.RemoteImages)
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	session := mustSession(c)
	auditResult := "success"
	if syncWarning != "" {
		auditResult = "degraded"
	}
	s.audit(c, &session.AdminID, session.Username, "inbox_sync", "alias", strconv.FormatInt(id, 10), auditResult, "")
	c.Header("Cache-Control", "no-store")
	writeAdminAPIData(c, http.StatusOK, gin.H{
		"alias":          s.buildInboxAliasDTO(c.Request.Context(), alias, true),
		"messages":       messages,
		"total_archived": totalArchived,
		"sync_warning":   syncWarning,
	})
}

func (s *Server) publicInboxMessages(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	alias, ok := s.authenticatePublicInboxToken(c)
	if !ok {
		return
	}
	release, ok := s.beginScopedMailboxPickupMode(c, alias.AccountID, alias.ID, "inbox", false)
	if !ok {
		return
	}
	defer release()

	tag := strings.TrimPrefix(strings.TrimSpace(c.Query("tag")), "+")
	if utf8.RuneCountInString(tag) > 128 {
		s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "tag 参数过长")
		return
	}
	allowRemoteImages := parseTruthyParam(c.Query("remote_images"))
	syncWarning := ""
	if parseTruthyParam(c.DefaultQuery("sync", "1")) {
		syncWarning = s.triggerOnDemandInboxSync(c.Request.Context(), alias, 8*time.Second)
		if refreshed, err := s.store.GetAlias(c.Request.Context(), alias.ID); err == nil {
			if !refreshed.Enabled || refreshed.AccountDisabled || !secure.HashEqual(refreshed.APIKeyHash, alias.APIKeyHash) {
				s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "查件分享链接已失效")
				return
			}
			alias = refreshed
		}
	}
	messages, totalArchived, err := s.loadInboxPreviewMessages(c.Request.Context(), alias, defaultInboxMessageLimit, tag, allowRemoteImages)
	if err != nil {
		s.logger.Error("读取公开收件分享归档失败", "alias_id", alias.ID, "error", err, "request_id", requestID(c))
		s.writeAPIError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "邮件归档暂不可用")
		return
	}
	if touchErr := s.store.TouchAliasAccess(c.Request.Context(), alias.ID, s.now().UTC()); touchErr != nil {
		s.logger.Warn("更新公开收件访问时间失败", "alias_id", alias.ID, "error", touchErr, "request_id", requestID(c))
	}
	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"alias":          s.buildInboxAliasDTO(c.Request.Context(), alias, false),
			"messages":       messages,
			"total_archived": totalArchived,
			"sync_warning":   syncWarning,
		},
	})
}

func (s *Server) authenticatePublicInboxToken(c *gin.Context) (domain.Alias, bool) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "查件分享链接无效")
		return domain.Alias{}, false
	}
	tokens := values["token"]
	if len(tokens) != 1 || !validAPIKey(tokens[0]) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "查件分享链接无效")
		return domain.Alias{}, false
	}
	token := tokens[0]
	aliasID, candidate := secure.InboxTokenAliasID(token)
	if !candidate {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "查件分享链接无效")
		return domain.Alias{}, false
	}
	alias, lookupErr := s.store.GetAlias(c.Request.Context(), aliasID)
	if errors.Is(lookupErr, store.ErrNotFound) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "查件分享链接无效")
		return domain.Alias{}, false
	}
	if lookupErr != nil {
		s.logger.Error("查询收件分享令牌失败", "error", lookupErr, "request_id", requestID(c))
		s.writeAPIError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "数据库暂不可用")
		return domain.Alias{}, false
	}
	if alias.CredentialMode != domain.AliasCredentialModeV2 ||
		!alias.Enabled || alias.AccountDisabled ||
		!s.cipher.VerifyInboxToken(token, aliasID, alias.APIKeyHash) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "查件分享链接已失效或已轮换")
		return domain.Alias{}, false
	}
	return alias, true
}

func (s *Server) buildInboxAliasDTO(ctx context.Context, alias domain.Alias, includeAdminFields bool) inboxAliasDTO {
	dto := inboxAliasDTO{
		ID:             alias.ID,
		Address:        alias.Address,
		Label:          alias.Label,
		Enabled:        alias.Enabled && !alias.AccountDisabled,
		AccountEnabled: !alias.AccountDisabled,
		LastSyncedAt:   adminAPIOptionalTime(alias.LastSyncedAt),
	}
	if includeAdminFields {
		dto.AccountID = alias.AccountID
		dto.AccountEmail = alias.AccountEmail
		dto.GroupID = alias.GroupID
		dto.GroupName = alias.GroupName
		dto.LatestReceivedAt = adminAPIOptionalTime(alias.LatestReceivedAt)
		if transport, err := s.store.GetAccountMailTransport(ctx, alias.AccountID); err == nil {
			dto.MailTransport = transport
		}
		if alias.CredentialMode == domain.AliasCredentialModeV2 && !adminAPIAliasConfirmationPending(alias) {
			if otpToken, err := s.cipher.OTPToken(alias.ID, alias.APIKeyHash); err == nil {
				dto.OTPURLPath = (&url.URL{Path: "/api/v1/otp", RawQuery: (url.Values{"token": {otpToken}}).Encode()}).String()
			}
			if inboxToken, err := s.cipher.InboxToken(alias.ID, alias.APIKeyHash); err == nil {
				dto.InboxSharePath = (&url.URL{Path: "/inbox/view", RawQuery: (url.Values{"token": {inboxToken}}).Encode()}).String()
			}
		}
	}
	return dto
}

func (s *Server) triggerOnDemandInboxSync(ctx context.Context, alias domain.Alias, timeout time.Duration) string {
	if !alias.Enabled || alias.AccountDisabled {
		return "该隐藏邮箱或所属主号当前已停用，仅展示本地已归档邮件。"
	}
	if s.demandAliasSync != nil {
		syncCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := s.demandAliasSync(syncCtx, alias.ID); err != nil {
			if ctx.Err() != nil {
				return ""
			}
			return "实时拉取上游新邮件暂未完成（可能处于冷却或连接受限），当前已展示本地最新归档。"
		}
		return ""
	}
	if s.sync != nil {
		err := s.sync(alias.AccountID)
		if err != nil && !errors.Is(err, syncer.ErrSyncPending) && !errors.Is(err, syncer.ErrSyncQueued) {
			return "触发主号后台同步未成功，当前已展示本地最新归档。"
		}
	}
	return ""
}

func (s *Server) loadInboxPreviewMessages(
	ctx context.Context,
	alias domain.Alias,
	limit int,
	tag string,
	allowRemoteImages bool,
) ([]inboxMessageDTO, int, error) {
	scanLimit := limit
	normalizedTag := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, "+")))
	if normalizedTag != "" {
		scanLimit = 20
	}
	archived, totalArchived, err := s.store.ListRecentArchivedMailboxMessages(ctx, alias.ID, scanLimit)
	if err != nil {
		return nil, 0, err
	}

	var latestFallback *domain.LatestMessage
	if latest, latestErr := s.store.GetLatestMessage(ctx, alias.ID); latestErr == nil {
		latestFallback = &latest
	}

	if len(archived) == 0 && latestFallback != nil {
		dto := s.previewFromLegacyLatestMessage(alias.Address, *latestFallback, allowRemoteImages)
		if normalizedTag != "" && !sliceContainsFold(dto.PlusTags, normalizedTag) {
			return []inboxMessageDTO{}, 1, nil
		}
		return []inboxMessageDTO{dto}, 1, nil
	}

	result := make([]inboxMessageDTO, 0, min(len(archived), limit))
	for _, msg := range archived {
		dto := s.previewFromArchivedMessage(alias.Address, msg, latestFallback, allowRemoteImages)
		if normalizedTag != "" && !sliceContainsFold(dto.PlusTags, normalizedTag) {
			continue
		}
		result = append(result, dto)
		if len(result) >= limit {
			break
		}
	}
	return result, totalArchived, nil
}

func (s *Server) previewFromArchivedMessage(
	rootAddress string,
	msg domain.ArchivedMailboxMessage,
	latestFallback *domain.LatestMessage,
	allowRemoteImages bool,
) inboxMessageDTO {
	from := nonNilAddresses(msg.From)
	to := nonNilAddresses(msg.To)
	cc := nonNilAddresses(msg.CC)
	attachments := []domain.Attachment{}
	subject := msg.Subject
	messageID := msg.MessageID
	sentAt := adminAPIOptionalTime(msg.HeaderDate)
	otp := msg.OTP
	var textBody, rawHTML string
	bodyTruncated := false
	contentState := msg.ContentState

	if file, err := s.store.OpenArchivedContentForPreview(msg); err == nil {
		parsed, parseErr := mail.ParseMIMEPreview(file, msg.ContentBytes, inboxPreviewBodyBudget)
		_ = file.Close()
		if parseErr == nil {
			if len(parsed.From) > 0 {
				from = parsed.From
			}
			if len(parsed.To) > 0 {
				to = parsed.To
			}
			if len(parsed.CC) > 0 {
				cc = parsed.CC
			}
			if parsed.Subject != "" {
				subject = parsed.Subject
			}
			if parsed.MessageID != "" {
				messageID = parsed.MessageID
			}
			if parsed.HeaderDate != nil {
				sentAt = adminAPIOptionalTime(parsed.HeaderDate)
			}
			if otp == "" && domain.IsSixDigitOTP(parsed.ExtractedOTP) {
				otp = parsed.ExtractedOTP
			}
			textBody = parsed.TextBody
			rawHTML = parsed.HTMLBody
			bodyTruncated = parsed.BodyTruncated
			if parsed.Attachments != nil {
				attachments = parsed.Attachments
			}
		}
	} else if latestFallback != nil && latestFallback.UID == msg.MailboxUID && latestFallback.UIDValidity == msg.UIDValidity {
		textBody = latestFallback.TextBody
		rawHTML = latestFallback.HTMLBody
		if len(latestFallback.Attachments) > 0 {
			attachments = latestFallback.Attachments
		}
		if textBody != "" || rawHTML != "" {
			contentState = "available"
		}
	}

	if !domain.IsSixDigitOTP(otp) {
		otp = mail.ExtractOTP(subject, textBody, rawHTML)
	}
	sanitized := sanitizeEmailHTML(rawHTML, allowRemoteImages)
	if textBody == "" && rawHTML != "" {
		textBody = strings.TrimSpace(plainTextFromHTML(rawHTML))
	}
	snippet := makeMailSnippet(textBody)
	plusTags := mail.ExtractPlusTags(rootAddress, to, cc)
	if plusTags == nil {
		plusTags = []string{}
	}

	return inboxMessageDTO{
		ID:                  msg.ID,
		MailboxUID:          msg.MailboxUID,
		MessageID:           messageID,
		Subject:             subject,
		From:                from,
		To:                  to,
		CC:                  cc,
		SentAt:              sentAt,
		ReceivedAt:          adminAPITime(msg.InternalDate),
		OTP:                 otp,
		Snippet:             snippet,
		TextBody:            textBody,
		SafeHTML:            sanitized.HTML,
		HasHTML:             sanitized.HTML != "",
		RemoteImagesBlocked: sanitized.RemoteImagesBlocked,
		ContentState:        contentState,
		ContentBytes:        msg.ContentBytes,
		BodyTruncated:       bodyTruncated,
		Attachments:         attachments,
		PlusTags:            plusTags,
	}
}

func (s *Server) previewFromLegacyLatestMessage(
	rootAddress string,
	latest domain.LatestMessage,
	allowRemoteImages bool,
) inboxMessageDTO {
	sanitized := sanitizeEmailHTML(latest.HTMLBody, allowRemoteImages)
	textBody := latest.TextBody
	if textBody == "" && latest.HTMLBody != "" {
		textBody = strings.TrimSpace(plainTextFromHTML(latest.HTMLBody))
	}
	otp := mail.ExtractOTP(latest.Subject, textBody, latest.HTMLBody)
	to := nonNilAddresses(latest.To)
	cc := nonNilAddresses(latest.CC)
	plusTags := mail.ExtractPlusTags(rootAddress, to, cc)
	if plusTags == nil {
		plusTags = []string{}
	}
	attachments := latest.Attachments
	if attachments == nil {
		attachments = []domain.Attachment{}
	}
	return inboxMessageDTO{
		ID:                  int64(latest.UID),
		MailboxUID:          latest.UID,
		MessageID:           latest.MessageID,
		Subject:             latest.Subject,
		From:                nonNilAddresses(latest.From),
		To:                  to,
		CC:                  cc,
		SentAt:              adminAPIOptionalTime(latest.HeaderDate),
		ReceivedAt:          adminAPITime(latest.InternalDate),
		OTP:                 otp,
		Snippet:             makeMailSnippet(textBody),
		TextBody:            textBody,
		SafeHTML:            sanitized.HTML,
		HasHTML:             sanitized.HTML != "",
		RemoteImagesBlocked: sanitized.RemoteImagesBlocked,
		ContentState:        "available",
		ContentBytes:        int64(len(latest.TextBody) + len(latest.HTMLBody)),
		Attachments:         attachments,
		PlusTags:            plusTags,
	}
}

func makeMailSnippet(text string) string {
	normalized := strings.TrimSpace(singleLinePlainText(text))
	if normalized == "" {
		return ""
	}
	runes := []rune(normalized)
	if len(runes) <= inboxSnippetMaxRunes {
		return normalized
	}
	return string(runes[:inboxSnippetMaxRunes]) + "…"
}

func nonNilAddresses(list []domain.MailAddress) []domain.MailAddress {
	if list == nil {
		return []domain.MailAddress{}
	}
	return list
}

func sliceContainsFold(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

func parseTruthyParam(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) publicInboxViewPage(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	c.Header("Content-Type", "text/html; charset=utf-8")

	values, queryErr := url.ParseQuery(c.Request.URL.RawQuery)
	tokens := values["token"]
	if queryErr != nil || len(tokens) != 1 || !validAPIKey(tokens[0]) {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusUnauthorized)
			return
		}
		c.Data(http.StatusUnauthorized, "text/html; charset=utf-8", renderPublicInboxErrorHTML("查件分享链接无效", "请确认分享链接完整且未被截断。"))
		return
	}
	token := tokens[0]
	aliasID, candidate := secure.InboxTokenAliasID(token)
	alias, lookupErr := domain.Alias{}, store.ErrNotFound
	if candidate {
		alias, lookupErr = s.store.GetAlias(c.Request.Context(), aliasID)
	}
	if lookupErr != nil || alias.CredentialMode != domain.AliasCredentialModeV2 ||
		!alias.Enabled || alias.AccountDisabled ||
		!s.cipher.VerifyInboxToken(token, aliasID, alias.APIKeyHash) {
		status := http.StatusUnauthorized
		title := "查件分享链接已失效"
		desc := "该邮箱凭据可能已轮换，或隐藏邮箱已被停用。"
		if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
			status = http.StatusServiceUnavailable
			title = "邮件服务暂不可用"
			desc = "请稍后刷新页面重试。"
		}
		if c.Request.Method == http.MethodHead {
			c.Status(status)
			return
		}
		c.Data(status, "text/html; charset=utf-8", renderPublicInboxErrorHTML(title, desc))
		return
	}
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}

	tag := strings.TrimPrefix(strings.TrimSpace(values.Get("tag")), "+")
	syncWarning := ""
	if s.tryAcquireHTMLInboxSync(alias.ID) {
		syncWarning = s.triggerOnDemandInboxSync(c.Request.Context(), alias, 6*time.Second)
		if refreshed, err := s.store.GetAlias(c.Request.Context(), alias.ID); err == nil {
			alias = refreshed
		}
	}

	messages, totalArchived, err := s.loadInboxPreviewMessages(c.Request.Context(), alias, defaultInboxMessageLimit, tag, false)
	if err != nil {
		c.Data(http.StatusServiceUnavailable, "text/html; charset=utf-8", renderPublicInboxErrorHTML("邮件归档读取失败", "请稍后刷新重试。"))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", renderPublicInboxHTML(alias, token, tag, messages, totalArchived, syncWarning))
}

func (s *Server) tryAcquireHTMLInboxSync(aliasID int64) bool {
	key := strconv.FormatInt(aliasID, 10) + "|inbox_html"
	now := s.now()
	s.aliasDemandRateMu.Lock()
	defer s.aliasDemandRateMu.Unlock()
	if s.aliasScopedDemandRate == nil {
		s.aliasScopedDemandRate = make(map[string]aliasDemandRateState)
	}
	state := s.aliasScopedDemandRate[key]
	if now.Before(state.nextAt) {
		return false
	}
	s.aliasScopedDemandRate[key] = aliasDemandRateState{nextAt: now.Add(3 * time.Second)}
	return true
}

func renderPublicInboxErrorHTML(title, description string) []byte {
	return []byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` +
		html.EscapeString(title) + ` · iCloud Mail</title><style>` + publicInboxStyles + `</style></head><body>` +
		`<main class="shell"><section class="card empty-card"><h1>` + html.EscapeString(title) + `</h1><p>` +
		html.EscapeString(description) + `</p></section></main></body></html>`)
}

func renderPublicInboxHTML(
	alias domain.Alias,
	token, tag string,
	messages []inboxMessageDTO,
	totalArchived int,
	syncWarning string,
) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>收件查看 · `)
	buf.WriteString(html.EscapeString(alias.Address))
	buf.WriteString(`</title><style>`)
	buf.WriteString(publicInboxStyles)
	buf.WriteString(`</style><script src="/inbox/view.js" defer></script></head><body data-token="`)
	buf.WriteString(html.EscapeString(token))
	buf.WriteString(`" data-tag="`)
	buf.WriteString(html.EscapeString(tag))
	buf.WriteString(`"><main class="shell"><header class="topbar card"><div><div class="eyebrow">iCloud Mail · 只读查件视图</div><h1>`)
	buf.WriteString(html.EscapeString(alias.Address))
	buf.WriteString(`</h1><div class="meta">`)
	if alias.Label != "" {
		buf.WriteString(`<span class="pill">`)
		buf.WriteString(html.EscapeString(alias.Label))
		buf.WriteString(`</span>`)
	}
	if tag != "" {
		buf.WriteString(`<span class="pill pill-accent">+`)
		buf.WriteString(html.EscapeString(tag))
		buf.WriteString(`</span>`)
	}
	buf.WriteString(`<span>最近 5 封（共归档 `)
	buf.WriteString(strconv.Itoa(totalArchived))
	buf.WriteString(` 封）</span></div></div><div class="actions"><button type="button" id="refresh-btn" class="btn btn-primary">刷新最新邮件</button></div></header>`)

	if syncWarning != "" {
		buf.WriteString(`<div id="sync-banner" class="banner">`)
		buf.WriteString(html.EscapeString(syncWarning))
		buf.WriteString(`</div>`)
	} else {
		buf.WriteString(`<div id="sync-banner" class="banner" hidden></div>`)
	}

	buf.WriteString(`<section id="messages-root" class="stack">`)
	if len(messages) == 0 {
		buf.WriteString(`<article class="card empty-card"><h2>暂无邮件</h2><p>当前未收到匹配邮件，点击右上角“刷新最新邮件”可立即同步。</p></article>`)
	}
	for _, msg := range messages {
		buf.WriteString(`<article class="card msg-card"><div class="msg-head"><div><h2>`)
		subj := msg.Subject
		if strings.TrimSpace(subj) == "" {
			subj = "(无主题)"
		}
		buf.WriteString(html.EscapeString(subj))
		buf.WriteString(`</h2><div class="meta"><span>发件人：`)
		buf.WriteString(html.EscapeString(formatAddressesText(msg.From)))
		buf.WriteString(`</span><span>时间：`)
		buf.WriteString(html.EscapeString(msg.ReceivedAt))
		buf.WriteString(`</span></div></div>`)
		if msg.OTP != "" {
			buf.WriteString(`<button type="button" class="otp-badge" data-copy-otp="`)
			buf.WriteString(html.EscapeString(msg.OTP))
			buf.WriteString(`">验证码 `)
			buf.WriteString(html.EscapeString(msg.OTP))
			buf.WriteString(` · 点击复制</button>`)
		}
		buf.WriteString(`</div>`)
		if msg.HasHTML && msg.SafeHTML != "" {
			srcdoc := `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>body{margin:0;padding:16px;font:14px/1.65 -apple-system,BlinkMacSystemFont,sans-serif;color:#1d232f;background:#fff;overflow-wrap:anywhere}img,table{max-width:100%;height:auto}a{color:#0066cc}</style></head><body>` + msg.SafeHTML + `</body></html>`
			buf.WriteString(`<iframe sandbox="" referrerpolicy="no-referrer" class="mail-frame" srcdoc="`)
			buf.WriteString(html.EscapeString(srcdoc))
			buf.WriteString(`" title="邮件正文"></iframe>`)
		} else if msg.TextBody != "" {
			buf.WriteString(`<pre class="plain">`)
			buf.WriteString(html.EscapeString(msg.TextBody))
			buf.WriteString(`</pre>`)
		} else {
			buf.WriteString(`<p class="muted">（无可用正文内容）</p>`)
		}
		buf.WriteString(`</article>`)
	}
	buf.WriteString(`</section><div id="toast" class="toast" hidden></div></main></body></html>`)
	return buf.Bytes()
}

func formatAddressesText(list []domain.MailAddress) string {
	if len(list) == 0 {
		return "未知发件人"
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		name := strings.TrimSpace(item.Name)
		email := strings.TrimSpace(item.Email)
		if name != "" && email != "" && !strings.EqualFold(name, email) {
			parts = append(parts, name+" <"+email+">")
		} else if email != "" {
			parts = append(parts, email)
		} else if name != "" {
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return "未知发件人"
	}
	return strings.Join(parts, ", ")
}

func (s *Server) publicInboxViewScript(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("Content-Type", "application/javascript; charset=utf-8")
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(publicInboxScript))
}

const publicInboxStyles = `
:root{color-scheme:light dark;--bg:#f3f5fa;--card:rgba(255,255,255,0.88);--border:rgba(15,23,42,0.1);--text:#17202e;--muted:#5b677a;--accent:#0066dc;--accent-soft:rgba(0,102,220,0.1);--plain:#f7f9fc}
@media(prefers-color-scheme:dark){:root{--bg:#12161f;--card:rgba(28,34,46,0.88);--border:rgba(255,255,255,0.12);--text:#eef2f8;--muted:#9aa7bd;--accent:#6cb2ff;--accent-soft:rgba(108,178,255,0.16);--plain:#1a202c}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC",sans-serif}
.shell{max-width:960px;margin:0 auto;padding:24px 16px 64px;display:flex;flex-direction:column;gap:14px}
.card{background:var(--card);border:1px solid var(--border);border-radius:18px;padding:18px 20px;backdrop-filter:blur(16px);box-shadow:0 8px 28px rgba(0,0,0,0.05)}
.topbar{display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:14px}
.eyebrow{font-size:12px;font-weight:600;color:var(--accent);margin-bottom:2px}
h1{margin:0;font-size:20px;word-break:break-all}h2{margin:0;font-size:16px}
.meta{display:flex;flex-wrap:wrap;gap:8px 14px;margin-top:6px;color:var(--muted);font-size:12px;align-items:center}
.pill{padding:2px 9px;border-radius:999px;background:var(--plain);border:1px solid var(--border);color:var(--text)}
.pill-accent{background:var(--accent-soft);color:var(--accent);border-color:transparent;font-weight:600}
.btn{border:1px solid var(--border);border-radius:10px;padding:8px 14px;font-size:13px;font-weight:600;cursor:pointer;background:var(--card);color:var(--text)}
.btn-primary{background:var(--accent);color:#fff;border-color:transparent}.btn:disabled{opacity:.6;cursor:wait}
.banner{padding:10px 14px;border-radius:12px;background:rgba(245,158,11,0.14);border:1px solid rgba(245,158,11,0.3);font-size:13px}
.stack{display:flex;flex-direction:column;gap:14px}
.msg-head{display:flex;justify-content:space-between;align-items:flex-start;flex-wrap:wrap;gap:12px;margin-bottom:12px}
.otp-badge{border:0;border-radius:10px;padding:7px 12px;background:var(--accent-soft);color:var(--accent);font:600 14px/1.2 ui-monospace,monospace;cursor:pointer}
.mail-frame{width:100%;min-height:280px;border:1px solid var(--border);border-radius:12px;background:#fff}
.plain{margin:0;padding:14px;border-radius:12px;background:var(--plain);border:1px solid var(--border);white-space:pre-wrap;word-break:break-word;font:13px/1.65 ui-monospace,monospace}
.empty-card{text-align:center;padding:36px 20px}.muted{color:var(--muted)}
.toast{position:fixed;bottom:24px;left:50%;transform:translateX(-50%);padding:8px 16px;border-radius:999px;background:#17202e;color:#fff;font-size:13px;box-shadow:0 6px 20px rgba(0,0,0,0.22)}
`

const publicInboxScript = `
(function () {
  var toastTimer = null;
  function showToast(text) {
    var el = document.getElementById("toast");
    if (!el) return;
    el.textContent = text;
    el.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { el.hidden = true; }, 2200);
  }
  function copyValue(val) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(val).then(function () {
        showToast("已复制验证码 " + val);
      }).catch(function () { fallbackCopy(val); });
      return;
    }
    fallbackCopy(val);
  }
  function fallbackCopy(val) {
    var ta = document.createElement("textarea");
    ta.value = val;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      showToast("已复制验证码 " + val);
    } catch (_) {
      showToast("复制失败，请手动复制");
    }
    document.body.removeChild(ta);
  }
  document.addEventListener("click", function (ev) {
    var btn = ev.target && ev.target.closest ? ev.target.closest("[data-copy-otp]") : null;
    if (btn) {
      copyValue(btn.getAttribute("data-copy-otp") || "");
    }
  });
  var refreshBtn = document.getElementById("refresh-btn");
  if (refreshBtn) {
    refreshBtn.addEventListener("click", function () {
      refreshBtn.disabled = true;
      refreshBtn.textContent = "正在同步…";
      window.location.reload();
    });
  }
})();
`
