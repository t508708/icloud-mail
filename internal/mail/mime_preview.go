package mail

import (
	"io"
	"strings"
	"time"

	"icloud-api/internal/domain"
)

// PreviewMessage holds bounded, decoded content extracted from an archived MIME
// stream for inbox previewing.
type PreviewMessage struct {
	MessageID     string
	HeaderDate    *time.Time
	From          []domain.MailAddress
	To            []domain.MailAddress
	CC            []domain.MailAddress
	Subject       string
	TextBody      string
	HTMLBody      string
	ExtractedOTP  string
	Attachments   []domain.Attachment
	BodyTruncated bool
}

// ParseMIMEPreview streams an archived MIME message with strict body, part, and
// memory budgets, extracting text/HTML bodies, attachments, and any valid OTP.
func ParseMIMEPreview(reader io.Reader, rawSize, maxBodyBytes int64) (PreviewMessage, error) {
	if maxBodyBytes <= 0 {
		maxBodyBytes = 512 << 10
	}
	limits := defaultMIMELimits(maxBodyBytes, maxBodyBytes+defaultMetadataResultBytes)
	parsed, err := parseMIMEMessageReader(reader, rawSize, limits)
	if err != nil {
		return PreviewMessage{}, err
	}
	otp := ExtractOTP(parsed.subject, parsed.textBody, parsed.htmlBody)
	return PreviewMessage{
		MessageID:     parsed.messageID,
		HeaderDate:    parsed.headerDate,
		From:          parsed.from,
		To:            parsed.to,
		CC:            parsed.cc,
		Subject:       parsed.subject,
		TextBody:      parsed.textBody,
		HTMLBody:      parsed.htmlBody,
		ExtractedOTP:  otp,
		Attachments:   parsed.attachments,
		BodyTruncated: parsed.bodyTruncated,
	}, nil
}

// ExtractPlusTags returns normalized +tag suffixes addressed to rootAddress
// (for example, "foo+chatgpt@icloud.com" for root "foo@icloud.com" yields "chatgpt").
func ExtractPlusTags(rootAddress string, recipients ...[]domain.MailAddress) []string {
	root := domain.NormalizeEmail(rootAddress)
	at := strings.LastIndexByte(root, '@')
	if at <= 0 {
		return nil
	}
	rootLocal, rootDomain := root[:at], root[at+1:]
	if strings.IndexByte(rootLocal, '+') >= 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var tags []string
	for _, group := range recipients {
		for _, item := range group {
			addr := domain.NormalizeEmail(item.Email)
			addrAt := strings.LastIndexByte(addr, '@')
			if addrAt <= 0 || addr[addrAt+1:] != rootDomain {
				continue
			}
			local := addr[:addrAt]
			plus := strings.IndexByte(local, '+')
			if plus <= 0 || local[:plus] != rootLocal {
				continue
			}
			tag := strings.TrimSpace(local[plus+1:])
			if tag == "" {
				continue
			}
			if _, exists := seen[tag]; !exists {
				seen[tag] = struct{}{}
				tags = append(tags, tag)
			}
		}
	}
	return tags
}
