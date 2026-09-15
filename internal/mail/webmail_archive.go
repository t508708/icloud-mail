package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

// ArchiveWebMail converts Apple's text parts to a MIME projection for the
// existing read API. It does not advance an IMAP cursor or claim a full MIME download.
func (f *Fetcher) ArchiveWebMail(ctx context.Context, account domain.Account, alias domain.Alias, known []domain.Alias, remote apple.WebMailResult) (domain.MailboxSyncResult, error) {
	result := domain.MailboxSyncResult{State: domain.IMAPSyncState{AccountID: account.ID, UIDValidity: remote.UIDValidity, UpdatedAt: time.Now().UTC()}}
	if !account.Enabled || !alias.Enabled || alias.AccountID != account.ID || remote.UIDValidity == 0 {
		return result, ErrInvalidAlias
	}
	addresses, err := prepareAliases(account, known, f.settings().maxAliases)
	if err != nil {
		return result, err
	}
	for _, message := range remote.Messages {
		if err := ctx.Err(); err != nil {
			return domain.MailboxSyncResult{}, err
		}
		if message.UID == 0 {
			return domain.MailboxSyncResult{}, errors.New("web mail UID is empty")
		}
		header, err := stdmail.ReadMessage(strings.NewReader(strings.TrimRight(message.LongHeader, "\r\n") + "\r\n\r\n"))
		if err != nil {
			return domain.MailboxSyncResult{}, errors.New("web mail delivery header invalid")
		}
		ids, valid := classifyArchiveRecipientAliases(header.Header, addresses, account, f.AllowWeakRecipientHeaders)
		matches := false
		for _, id := range ids {
			matches = matches || id == alias.ID
		}
		if !valid || !matches {
			continue
		}
		raw, err := webMailProjection(header.Header, message)
		if err != nil {
			return domain.MailboxSyncResult{}, err
		}
		parsed, err := parseMIMEMessage(raw, int64(f.settings().maxBodyBytes))
		if err != nil {
			return domain.MailboxSyncResult{}, err
		}
		received := message.ReceivedAt
		if received.IsZero() && parsed.headerDate != nil {
			received = *parsed.headerDate
		}
		if received.IsZero() {
			return domain.MailboxSyncResult{}, errors.New("web mail received time missing")
		}
		archived := domain.ArchivedMessage{AccountID: account.ID, UIDValidity: remote.UIDValidity, UID: message.UID,
			InternalDate: received.UTC(), RawMIME: raw, RawSize: int64(len(raw)), ContentState: domain.ArchiveContentAvailable,
			AliasIDs: []int64{alias.ID}, SyncedAt: result.State.UpdatedAt}
		projectParsedArchiveContent(&archived, parsed)
		archived.BodyTruncated = archived.BodyTruncated || message.BodyTruncated
		archived.OTP = ExtractOTP(archived.Subject, archived.TextBody, archived.HTMLBody)
		result.ArchivedMessages = append(result.ArchivedMessages, archived)
		result.State.LastUID = max(result.State.LastUID, archived.UID)
	}
	sort.Slice(result.ArchivedMessages, func(i, j int) bool { return result.ArchivedMessages[i].UID < result.ArchivedMessages[j].UID })
	return result, nil
}

func webMailProjection(header stdmail.Header, message apple.WebMailMessage) ([]byte, error) {
	var raw bytes.Buffer
	for _, key := range []string{"From", "To", "Cc", "Subject", "Date", "Message-Id"} {
		value := header.Get(key)
		if strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("web mail header contains invalid line break")
		}
		if value != "" {
			fmt.Fprintf(&raw, "%s: %s\r\n", key, value)
		}
	}
	writer := multipart.NewWriter(&raw)
	fmt.Fprintf(&raw, "MIME-Version: 1.0\r\nX-Mail-Archive-Source: icloud-web-text-parts\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", writer.Boundary())
	for _, part := range []struct{ kind, content string }{{"text/plain", message.TextBody}, {"text/html", message.HTMLBody}} {
		if part.content == "" && part.kind == "text/html" {
			continue
		}
		out, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		encoded := quotedprintable.NewWriter(out)
		if _, err := encoded.Write([]byte(part.content)); err != nil {
			return nil, err
		}
		if err := encoded.Close(); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return raw.Bytes(), nil
}
