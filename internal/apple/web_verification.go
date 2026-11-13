package apple

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebVerification is confined to a pending, owner-bound login challenge.
// Full phone numbers and upstream HTML are never retained.
type WebVerification struct {
	Method               string         `json:"method"`
	PhoneID              int64          `json:"phone_id,omitempty"`
	Phones               []TrustedPhone `json:"phones,omitempty"`
	Sent                 bool           `json:"sent"`
	DiscoveryStatus      int            `json:"discovery_status,omitempty"`
	DeliveryStatus       int            `json:"delivery_status,omitempty"`
	RequestAfter         time.Time      `json:"request_after,omitempty"`
	OptionsSource        string         `json:"options_source,omitempty"`
	PhoneListPresent     bool           `json:"phone_list_present"`
	RawPhoneCount        int            `json:"raw_phone_count"`
	InvalidPhoneCount    int            `json:"invalid_phone_count"`
	SMSHidden            bool           `json:"sms_hidden"`
	SelectedPhonePresent bool           `json:"selected_phone_present"`
	AutomaticSMS         bool           `json:"automatic_sms"`
	InitialPhoneRoute    bool           `json:"initial_phone_route"`
	DeliveryFailed       bool           `json:"delivery_failed"`
}

type TrustedPhone struct {
	ID            int64  `json:"id"`
	NonFTEU       *bool  `json:"non_fteu,omitempty"`
	LastTwoDigits string `json:"last_two_digits,omitempty"`
}

func (p TrustedPhone) verificationPayload() map[string]any {
	out := map[string]any{"id": p.ID}
	if p.NonFTEU != nil {
		out["nonFTEU"] = *p.NonFTEU
	}
	return out
}

func (v *WebVerification) selectedPhone(id int64) (TrustedPhone, bool) {
	for _, phone := range v.Phones {
		if phone.ID == id {
			return phone, true
		}
	}
	return TrustedPhone{}, false
}

func cloneWebVerification(v *WebVerification) *WebVerification {
	if v == nil {
		return &WebVerification{}
	}
	copy := *v
	copy.Phones = append([]TrustedPhone(nil), v.Phones...)
	return &copy
}

func (op *operation) prepareWebVerification(ctx context.Context) error {
	state := &WebVerification{Method: "device"}
	op.session.WebVerification = state
	headers := op.authHeaders()
	headers.Set("Accept", "text/html,application/json")
	headers.Del("Content-Type")
	headers.Del("Origin")
	r, err := op.request(ctx, "read two-factor options", http.MethodGet, op.endpoints.Auth, nil, headers)
	state.DiscoveryStatus = r.status
	if err == nil && r.status == http.StatusOK {
		parsed := decodeWebVerification(r.body)
		parsed.DiscoveryStatus = r.status
		state = parsed
		op.session.WebVerification = state
	} else if err != nil && ctx.Err() != nil {
		return err
	} else if r.status == http.StatusTooManyRequests || r.status >= 500 {
		return r.operationError("read two-factor options", ErrService, nil)
	} else if r.status == 401 || r.status == 403 || r.status == 419 {
		return r.operationError("read two-factor options", ErrInvalidSession, nil)
	}
	if state.SelectedPhonePresent || state.InitialPhoneRoute && len(state.Phones) == 1 {
		// Apple's selected phone can belong to an SMS already dispatched during
		// sign-in. Offer verification without assuming delivery or sending twice.
		state.Method = "sms"
		if !state.SelectedPhonePresent {
			state.PhoneID = state.Phones[0].ID
		}
		state.RequestAfter = time.Now().Add(time.Minute)
		return nil
	}
	if state.SMSHidden && len(state.Phones) > 0 {
		state.Method = "sms"
		if !state.SelectedPhonePresent {
			if len(state.Phones) == 1 {
				state.PhoneID = state.Phones[0].ID
			} else {
				state.Method = ""
			}
		}
		return nil
	}
	finishSend := func(method string, phoneID int64) error {
		if sendErr := op.sendWebVerification(ctx, method, phoneID); sendErr != nil {
			if ctx.Err() != nil {
				return sendErr
			}
			// Credentials were accepted. Preserve their pending challenge even
			// when delivery is uncertain, and expose the failure in its metadata.
			state.DeliveryFailed = true
		}
		return nil
	}
	if len(state.Phones) == 1 {
		return finishSend("sms", state.Phones[0].ID)
	}
	if len(state.Phones) > 1 {
		// Several numbers require an explicit user choice; never text a random one.
		state.Method = ""
		return nil
	}
	return finishSend("device", 0)
}

// RequestWebVerification sends once for the existing challenge. It never signs
// in again or falls back to another recipient after an ambiguous send result.
func (c *Client) RequestWebVerification(ctx context.Context, session Session, method string, phoneID int64) (result Session, err error) {
	result = session
	result.WebVerification = cloneWebVerification(session.WebVerification)
	op, err := c.newOperationForContext(ctx, &result)
	if err != nil {
		return result, err
	}
	defer op.persist(&result)
	err = op.sendWebVerification(ctx, method, phoneID)
	return result, err
}

func (op *operation) sendWebVerification(ctx context.Context, method string, phoneID int64) error {
	v := op.session.WebVerification
	if v == nil {
		v = &WebVerification{}
		op.session.WebVerification = v
	}
	return sendVerification(ctx, v, method, phoneID, func(ctx context.Context, httpMethod, path string, payload any) (responseData, error) {
		return op.request(ctx, "request "+method+" two-factor code", httpMethod, op.endpoints.Auth+path, payload, op.authHeaders())
	})
}

type verificationRequest func(context.Context, string, string, any) (responseData, error)

func sendVerification(ctx context.Context, v *WebVerification, method string, phoneID int64, request verificationRequest) error {
	if method != "sms" && method != "device" {
		return operationError("select two-factor method", ErrInvalidConfig, 0, nil)
	}
	var payload any
	path := "/verify/trusteddevice/securitycode"
	if method == "sms" {
		if _, ok := v.selectedPhone(phoneID); !ok {
			return operationError("select trusted phone", ErrInvalidConfig, 0, nil)
		}
		if v.SMSHidden {
			return operationError("SMS resend not offered", ErrService, http.StatusConflict, nil)
		}
		path = "/verify/phone"
		payload = map[string]any{"phoneNumber": map[string]any{"id": phoneID}, "mode": "sms"}
	} else {
		phoneID = 0
	}
	if now := time.Now(); now.Before(v.RequestAfter) {
		return &Error{Op: "request two-factor code cooldown", Kind: ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: v.RequestAfter.Sub(now)}
	}
	// Reserve before I/O: a timeout may still have delivered a message.
	v.RequestAfter = time.Now().Add(time.Minute)
	v.Method, v.PhoneID, v.Sent, v.DeliveryStatus, v.DeliveryFailed = method, phoneID, false, 0, false
	r, err := request(ctx, http.MethodPut, path, payload)
	v.DeliveryStatus = r.status
	if r.status == http.StatusPreconditionFailed && method == "device" {
		parsed := decodeWebVerification(r.body)
		if len(parsed.Phones) > 0 {
			v.Phones = parsed.Phones
			v.PhoneListPresent = parsed.PhoneListPresent
			v.RawPhoneCount = parsed.RawPhoneCount
			v.OptionsSource = "device_response"
			v.SMSHidden = parsed.SMSHidden
			v.SelectedPhonePresent = parsed.SelectedPhonePresent
			v.AutomaticSMS = parsed.AutomaticSMS
			if parsed.SelectedPhonePresent {
				v.PhoneID = parsed.PhoneID
				v.Method = "sms"
			}
		}
	}
	if delay := parseRetryAfter(r.header.Get("Retry-After"), time.Now()); delay > 0 {
		v.RequestAfter = maxTime(v.RequestAfter, time.Now().Add(delay))
	}
	if err != nil {
		return err
	}
	if r.status == http.StatusMethodNotAllowed && method == "device" {
		// Some Apple device flows dispatch during sign-in and reject a resend.
		// Keep the input usable, while explicitly not claiming a confirmed send.
		return nil
	}
	if r.status != http.StatusOK && r.status != http.StatusNoContent {
		return r.operationError("request "+method+" two-factor code", ErrService, nil)
	}
	var envelope struct {
		ServiceErrors []json.RawMessage `json:"serviceErrors"`
		Success       json.RawMessage   `json:"success"`
	}
	if r.status == http.StatusOK && (len(bytes.TrimSpace(r.body)) == 0 || json.Unmarshal(r.body, &envelope) != nil || bytes.TrimSpace(r.body)[0] != '{') {
		return r.operationError("request "+method+" two-factor code", ErrInvalidResponse, nil)
	}
	if len(envelope.ServiceErrors) > 0 || bytes.Equal(envelope.Success, []byte("false")) || bytes.Equal(envelope.Success, []byte("0")) || responseServiceCode(r.body) != "" && responseServiceCode(r.body) != "0" {
		return r.operationError("request "+method+" two-factor code", ErrService, nil)
	}
	v.Sent = true
	return nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Parse only known authentication containers, never arbitrary nested phone data.
func decodeTrustedPhones(body []byte) []TrustedPhone { return decodeWebVerification(body).Phones }

func decodeWebVerification(body []byte) *WebVerification {
	state := &WebVerification{Method: "device", OptionsSource: "unrecognized"}
	var roots []map[string]json.RawMessage
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) == nil && root != nil {
		roots = append(roots, root)
		state.OptionsSource = "json"
	} else {
		z := html.NewTokenizer(bytes.NewReader(body))
		for {
			kind := z.Next()
			if kind == html.ErrorToken {
				break
			}
			if kind != html.StartTagToken {
				continue
			}
			token := z.Token()
			if token.Data != "script" {
				continue
			}
			id, class, mime := "", "", ""
			for _, a := range token.Attr {
				if a.Key == "id" {
					id = a.Val
				}
				if a.Key == "class" {
					class = a.Val
				}
				if a.Key == "type" {
					mime = a.Val
				}
			}
			bootClass := false
			for _, name := range strings.Fields(class) {
				bootClass = bootClass || name == "boot_args"
			}
			if id != "app_config" && id != "boot_args" && !(bootClass && mime == "application/json") {
				continue
			}
			var candidate map[string]json.RawMessage
			if z.Next() != html.TextToken || json.Unmarshal(z.Text(), &candidate) != nil {
				continue
			}
			roots = append(roots, candidate)
			state.OptionsSource = "html_boot"
		}
	}
	seen := map[int64]bool{}
	parsePhone := func(raw json.RawMessage) (TrustedPhone, bool) {
		var entry map[string]json.RawMessage
		if json.Unmarshal(raw, &entry) != nil {
			return TrustedPhone{}, false
		}
		text := string(entry["id"])
		if len(text) > 0 && text[0] == '"' {
			if json.Unmarshal(entry["id"], &text) != nil {
				return TrustedPhone{}, false
			}
		}
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id < 0 || id > 9007199254740991 {
			return TrustedPhone{}, false
		}
		var mode string
		_ = json.Unmarshal(entry["pushMode"], &mode)
		if mode != "" && !strings.EqualFold(mode, "sms") {
			return TrustedPhone{}, false
		}
		phone := TrustedPhone{ID: id}
		var nonFTEU bool
		if json.Unmarshal(entry["nonFTEU"], &nonFTEU) == nil {
			phone.NonFTEU = &nonFTEU
		}
		_ = json.Unmarshal(entry["lastTwoDigits"], &phone.LastTwoDigits)
		tail := phone.LastTwoDigits
		if len(tail) != 2 || tail[0] < '0' || tail[0] > '9' || tail[1] < '0' || tail[1] > '9' {
			phone.LastTwoDigits = ""
		}
		return phone, true
	}
	addPhone := func(p TrustedPhone) {
		if !seen[p.ID] && len(state.Phones) < 20 {
			seen[p.ID] = true
			state.Phones = append(state.Phones, p)
		}
	}
	var walk func(map[string]json.RawMessage, int, bool)
	walk = func(node map[string]json.RawMessage, depth int, phoneContext bool) {
		if depth > 8 || len(state.Phones) >= 20 {
			return
		}
		var route string
		if json.Unmarshal(node["authInitialRoute"], &route) == nil && route == "/auth/verify/phone" {
			state.InitialPhoneRoute = true
		}
		var hidden bool
		if json.Unmarshal(node["hideSendSMSCodeOption"], &hidden) == nil && hidden {
			state.SMSHidden = true
		}
		if raw, ok := node["trustedPhoneNumbers"]; ok {
			state.PhoneListPresent = true
			var list []json.RawMessage
			if json.Unmarshal(raw, &list) == nil {
				state.RawPhoneCount += len(list)
				for _, entry := range list {
					if p, ok := parsePhone(entry); ok {
						addPhone(p)
					} else {
						state.InvalidPhoneCount++
					}
				}
			}
		}
		if phoneContext {
			for _, key := range []string{"trustedPhoneNumber", "phoneNumber"} {
				if p, ok := parsePhone(node[key]); ok {
					addPhone(p)
					state.PhoneID = p.ID
					state.SelectedPhonePresent = true
					var success bool
					var numeric int
					if json.Unmarshal(node["success"], &success) == nil && success || json.Unmarshal(node["success"], &numeric) == nil && numeric == 1 {
						state.AutomaticSMS = true
					}
				}
			}
		}
		for _, key := range []string{"bootData", "direct", "twoSV", "trustedDeviceVerification", "phoneNumberVerification", "authFactors"} {
			var child map[string]json.RawMessage
			if json.Unmarshal(node[key], &child) == nil {
				walk(child, depth+1, phoneContext || key == "phoneNumberVerification")
			}
		}
	}
	for _, root := range roots {
		walk(root, 0, false)
	}
	if state.SelectedPhonePresent {
		state.Method = "sms"
	} else if state.InitialPhoneRoute {
		if len(state.Phones) == 1 {
			state.Method = "sms"
			state.PhoneID = state.Phones[0].ID
		} else if len(state.Phones) > 1 {
			state.Method = ""
		}
	}
	return state
}
