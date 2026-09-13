package apple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrWebMailAuthentication = errors.New("web mail authentication required")

type WebMailResult struct {
	UIDValidity uint32
	Messages    []WebMailMessage
}
type WebMailMessage struct {
	UID                            uint32
	LongHeader, TextBody, HTMLBody string
	ReceivedAt                     time.Time
	BodyTruncated                  bool
}

const webMailMaxBody = 512 << 10

func (c *Client) ReadAliasMail(ctx context.Context, session Session, aliasAddress string) (WebMailResult, Session, error) {
	var result WebMailResult
	checkpoint := session
	if session.DSID == "" || len(session.Cookies) == 0 {
		return result, checkpoint, operationError("read web mail", ErrWebMailAuthentication, 0, nil)
	}
	alias, err := mail.ParseAddress(strings.TrimSpace(aliasAddress))
	if err != nil || !strings.EqualFold(alias.Address, strings.TrimSpace(aliasAddress)) {
		return result, checkpoint, fmt.Errorf("invalid alias address")
	}
	op, err := c.newOperation(&session)
	if err != nil {
		return result, checkpoint, err
	}
	base, err := webMailBase(session)
	if err != nil {
		return result, checkpoint, err
	}
	endpoint := func(path string) string { return webMailEndpoint(base, path, session) }
	var folders struct {
		DomainObjects []struct {
			Identifier  string          `json:"identifier"`
			Name        string          `json:"name"`
			UIDValidity json.RawMessage `json:"uidValidity"`
		} `json:"domainObjects"`
	}
	body := map[string]any{"domain": "mailbox", "includeLabels": true, "predicate": map[string]any{"type": "eq", "expression": map[string]any{"type": "property", "property": "isMboxDeleted"}, "value": false}, "properties": []string{"identifier", "name", "uidValidity"}}
	if err = webMailJSON(ctx, op, endpoint("/mailws2/v1/geqs/query"), body, &folders); err != nil {
		return result, checkpoint, err
	}
	var inbox struct {
		id          string
		uidValidity uint32
	}
	for _, f := range folders.DomainObjects {
		if f.Name == "INBOX" {
			inbox.id = f.Identifier
			inbox.uidValidity = rawUint32(f.UIDValidity)
			break
		}
	}
	if inbox.id == "" || inbox.uidValidity == 0 {
		return result, checkpoint, operationError("locate web mail inbox", ErrInvalidResponse, 0, nil)
	}
	result.UIDValidity = inbox.uidValidity
	var search struct {
		ThreadList []struct {
			ThreadID string `json:"threadId"`
		} `json:"threadList"`
	}
	if err = webMailJSON(ctx, op, endpoint("/mailws2/v1/thread/search"), map[string]any{"responseType": "THREAD_DIGEST", "includeFolderStatus": false, "maxResults": 20, "sessionHeaders": map[string]any{"folder": "INBOX", "modseq": nil, "threadmodseq": nil, "condstore": 1, "qresync": 1, "threadmode": 1}}, &search); err != nil {
		return WebMailResult{}, checkpoint, err
	}
	if len(search.ThreadList) > 20 {
		return WebMailResult{}, checkpoint, fmt.Errorf("web mail thread limit exceeded")
	}
	if search.ThreadList == nil {
		return WebMailResult{}, checkpoint, operationError("search web mail", ErrInvalidResponse, 0, nil)
	}
	seenUIDs := make(map[uint32]bool)
	bodyBytes, metadataCount := 0, 0
	for _, t := range search.ThreadList {
		if t.ThreadID == "" || len(t.ThreadID) > 1024 {
			return WebMailResult{}, checkpoint, operationError("read web mail thread", ErrInvalidResponse, 0, nil)
		}
		var thread struct {
			MessageMetadataList []struct {
				UID     json.RawMessage `json:"uid"`
				Folder  string          `json:"folder"`
				Subject string          `json:"subject"`
				Date    json.RawMessage `json:"date"`
				From    json.RawMessage `json:"from"`
				To      json.RawMessage `json:"to"`
				CC      json.RawMessage `json:"cc"`
				BCC     json.RawMessage `json:"bcc"`
				Parts   []struct {
					PartID      string `json:"partId"`
					ContentType string `json:"contentType"`
					IsAttach    bool   `json:"isAttach"`
					FileName    string `json:"fileName"`
				} `json:"parts"`
			} `json:"messageMetadataList"`
		}
		if err = webMailJSON(ctx, op, endpoint("/mailws2/v1/thread/get"), map[string]any{"threadId": t.ThreadID, "sessionHeaders": map[string]any{"folder": "INBOX", "modseq": nil, "threadmodseq": nil, "condstore": 1, "qresync": 1, "threadmode": 1}}, &thread); err != nil {
			return WebMailResult{}, checkpoint, err
		}
		metadataCount += len(thread.MessageMetadataList)
		if thread.MessageMetadataList == nil || len(thread.MessageMetadataList) > 20 || metadataCount > 128 {
			return WebMailResult{}, checkpoint, fmt.Errorf("web mail message limit exceeded")
		}
		for _, m := range thread.MessageMetadataList {
			folder := m.Folder
			if _, suffix, ok := strings.Cut(folder, ":"); ok {
				folder = suffix
			}
			// Conversations may span Sent/Junk. Their UIDs do not identify INBOX messages.
			if folder != "" && folder != "INBOX" {
				continue
			}
			if !webMailRecipientMatch(alias.Address, m.To, m.CC, m.BCC) {
				continue
			}
			uid := rawUint32(m.UID)
			if uid == 0 {
				return WebMailResult{}, checkpoint, fmt.Errorf("web mail message missing uid")
			}
			if seenUIDs[uid] {
				continue
			}
			seenUIDs[uid] = true
			parts := make([]string, 0, 8)
			types := make([]string, 0, 8)
			omitted := false
			for _, p := range m.Parts {
				if p.IsAttach || p.FileName != "" {
					omitted = true
					continue
				}
				kind, _, typeErr := mime.ParseMediaType(p.ContentType)
				if typeErr == nil && (kind == "text/plain" || kind == "text/html") {
					if p.PartID == "" || len(parts) == 8 {
						return WebMailResult{}, checkpoint, fmt.Errorf("web mail part limit exceeded")
					}
					parts = append(parts, p.PartID)
					types = append(types, kind)
				} else {
					omitted = true
				}
			}
			msg := WebMailMessage{UID: uid, ReceivedAt: rawTime(m.Date), BodyTruncated: omitted}
			// Fetch one text part at a time: GUIDs and response ordering are opaque.
			requests := parts
			if len(requests) == 0 {
				requests = []string{""}
			}
			for i, partID := range requests {
				requested := []string{}
				if partID != "" {
					requested = []string{partID}
				}
				var detail struct {
					LongHeader string `json:"longHeader"`
					Parts      []struct {
						Content string `json:"content"`
					} `json:"parts"`
				}
				if err = webMailJSON(ctx, op, endpoint("/mailws2/v1/message/get"), map[string]any{"uid": strconv.FormatUint(uint64(uid), 10), "parts": requested, "dontMarkAsRead": true, "sessionHeaders": map[string]any{"folder": "INBOX", "modseq": nil, "threadmodseq": nil, "condstore": 1, "qresync": 1, "threadmode": 1}}, &detail); err != nil {
					return WebMailResult{}, checkpoint, err
				}
				if detail.LongHeader == "" || len(detail.LongHeader) > 128<<10 || len(detail.Parts) != len(requested) || (msg.LongHeader != "" && detail.LongHeader != msg.LongHeader) {
					return WebMailResult{}, checkpoint, operationError("decode web mail message", ErrInvalidResponse, 0, nil)
				}
				msg.LongHeader = detail.LongHeader
				for _, p := range detail.Parts {
					bodyBytes += len(p.Content)
					if bodyBytes > webMailMaxBody {
						return WebMailResult{}, checkpoint, operationError("read web mail body", ErrResponseTooLarge, 0, nil)
					}
					if types[i] == "text/html" {
						msg.HTMLBody += p.Content
					} else {
						msg.TextBody += p.Content
					}
				}
			}
			result.Messages = append(result.Messages, msg)
		}
	}
	op.persist(&session)
	return result, session, nil
}

func webMailBase(s Session) (string, error) {
	for _, raw := range []string{s.MailGatewayURL, strings.Replace(strings.TrimRight(s.MailURL, "/"), "-mailws.", "-mccgateway.", 1), strings.Replace(strings.TrimRight(s.PremiumMailSettingsURL, "/"), "-maildomainws.", "-mccgateway.", 1)} {
		if raw == "" {
			continue
		}
		u, e := url.Parse(raw)
		if e == nil && validICloudServiceURL(u) && (strings.Contains(u.Hostname(), "-mccgateway.") || strings.Contains(u.Hostname(), "-mailws.") || strings.Contains(u.Hostname(), "-maildomainws.")) {
			if !strings.Contains(u.Hostname(), "-mccgateway.") {
				continue
			}
			return strings.TrimRight(u.String(), "/"), nil
		}
	}
	return "", fmt.Errorf("web mail service unavailable")
}
func webMailEndpoint(base, path string, s Session) string {
	u, _ := url.Parse(strings.TrimRight(base, "/") + path)
	q := u.Query()
	q.Set("clientBuildNumber", "2622Build20")
	q.Set("clientMasteringNumber", "2622Build20")
	q.Set("clientId", s.ClientID)
	q.Set("dsid", s.DSID)
	if path == "/mailws2/v1/geqs/query" {
		q.Set("clientIntent", "fetchMailboxCountQuery")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func webMailJSON(ctx context.Context, op *operation, raw string, body, out any) error {
	headers := op.serviceHeaders()
	headers.Set("Sec-Fetch-Site", "same-site")
	headers.Set("Sec-Fetch-Mode", "cors")
	headers.Set("Sec-Fetch-Dest", "empty")
	u, _ := url.Parse(raw)
	operationName := "web mail " + u.Path
	if u.Path == "/mailws2/v1/geqs/query" {
		headers.Set("Content-Type", "text/plain;charset=UTF-8")
	}
	r, e := op.request(ctx, operationName, http.MethodPost, raw, body, headers)
	if e != nil {
		return e
	}
	if r.status < 200 || r.status >= 300 {
		kind := ErrService
		if r.status == 401 || r.status == 403 || r.status == 421 || r.status == 450 {
			kind = ErrWebMailAuthentication
		}
		return r.operationError(operationName, kind, nil)
	}
	var envelope struct {
		ErrorCode     json.RawMessage   `json:"errorCode"`
		ServiceErrors []json.RawMessage `json:"serviceErrors"`
		Success       *bool             `json:"success"`
	}
	if json.Unmarshal(r.body, &envelope) != nil {
		return r.operationError(operationName, ErrInvalidResponse, nil)
	}
	code := strings.Trim(string(envelope.ErrorCode), `"`)
	if (code != "" && code != "null" && code != "0") || len(envelope.ServiceErrors) > 0 || (envelope.Success != nil && !*envelope.Success) {
		return r.operationError(operationName, ErrService, nil)
	}
	if e = json.Unmarshal(r.body, out); e != nil {
		return r.operationError(operationName, ErrInvalidResponse, nil)
	}
	return nil
}
func webMailRecipientMatch(want string, values ...json.RawMessage) bool {
	for _, raw := range values {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		var walk func(any) bool
		walk = func(x any) bool {
			switch z := x.(type) {
			case string:
				a, e := mail.ParseAddress(z)
				return e == nil && strings.EqualFold(a.Address, want)
			case []any:
				for _, i := range z {
					if walk(i) {
						return true
					}
				}
			case map[string]any:
				if email, ok := z["email"].(string); ok {
					return walk(email)
				}
			}
			return false
		}
		if walk(v) {
			return true
		}
	}
	return false
}
func rawUint32(r json.RawMessage) uint32 {
	var n uint64
	if json.Unmarshal(r, &n) == nil && n > 0 && n <= ^uint64(0)>>32 {
		return uint32(n)
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		x, err := strconv.ParseUint(s, 10, 32)
		if err == nil && x > 0 {
			return uint32(x)
		}
	}
	return 0
}
func rawTime(r json.RawMessage) time.Time {
	var n int64
	var text string
	if json.Unmarshal(r, &text) == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC()
		}
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			n = parsed
		}
	}
	if n != 0 || json.Unmarshal(r, &n) == nil {
		if n > 1e12 {
			return time.UnixMilli(n).UTC()
		}
		if n > 1e9 {
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Time{}
}
