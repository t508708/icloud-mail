package apple

import (
	"context"
	"errors"
)

// Account verification keeps its own widget headers, jar and session. Only the
// phone parsing and delivery rules are shared with iCloud Web.
func (c *Client) RequestAccountVerification(ctx context.Context, session AccountSession, method string, phoneID int64) (AccountSession, error) {
	s := session
	s.Verification = cloneWebVerification(session.Verification)
	err := sendVerification(ctx, s.Verification, method, phoneID, func(ctx context.Context, httpMethod, path string, payload any) (responseData, error) {
		r, err := c.accountCall(ctx, &s, httpMethod, accountAuth+path, payload, accountHeaders(s, true), false)
		if err != nil {
			// This is an in-memory pending login, not a persisted authenticated
			// management session. Keep rotated anti-replay headers for its next step.
			if value := r.header.Get("scnt"); value != "" {
				s.SCNT = value
			}
			if value := r.header.Get("X-Apple-ID-Session-Id"); value != "" {
				s.SessionID = value
			}
			var upstream *Error
			if errors.As(err, &upstream) {
				upstream.Op = "request " + method + " two-factor code"
				if upstream.StatusCode == 412 {
					upstream.Kind = ErrService
				}
			}
		}
		return r, err
	})
	if err != nil {
		s.Verification.DeliveryFailed = true
	}
	return s, err
}
