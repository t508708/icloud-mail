package domain

import (
	"errors"
	"strings"
)

var ErrIMAPAuthenticationPaused = errors.New("IMAP 认证异常，已暂停自动连接；更新收件凭据或重新启用主号后再试")

// IsIMAPAuthenticationFailure recognizes explicit IMAP authentication errors,
// not generic transport failures that should continue through retry handling.
func IsIMAPAuthenticationFailure(message string) bool {
	if message == ErrIMAPAuthenticationPaused.Error() {
		return true
	}
	message = strings.ToLower(message)
	for _, marker := range []string{"authenticationfailed", "authorizationfailed"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	imapContext := strings.Contains(message, "imap")
	loginContext := strings.Contains(message, "login")
	return (imapContext || loginContext) &&
		(strings.Contains(message, "authentication failed") || strings.Contains(message, "user disabled"))
}
