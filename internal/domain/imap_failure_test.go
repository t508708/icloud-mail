package domain

import "testing"

func TestIsIMAPAuthenticationFailure(t *testing.T) {
	for _, message := range []string{
		"AUTHENTICATIONFAILED Invalid credentials",
		"[AUTHORIZATIONFAILED] policy rejected login",
		"Login failed: Authentication Failed",
		"IMAP error: user disabled",
		ErrIMAPAuthenticationPaused.Error(),
	} {
		if !IsIMAPAuthenticationFailure(message) {
			t.Errorf("IsIMAPAuthenticationFailure(%q) = false", message)
		}
	}
	for _, message := range []string{"connection reset", "temporary transport failure", "authentication service timeout", "Apple authentication failed", "SMTP authentication failed", "user disabled connection timeout"} {
		if IsIMAPAuthenticationFailure(message) {
			t.Errorf("IsIMAPAuthenticationFailure(%q) = true", message)
		}
	}
}
