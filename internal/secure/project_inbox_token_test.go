package secure

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestInboxAndProjectOTPTokensArePurposeBoundAndRevocable(t *testing.T) {
	cipher, err := NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	hashA := sha256.Sum256([]byte("api-key-a"))
	hashB := sha256.Sum256([]byte("api-key-b"))

	inboxTok, err := cipher.InboxToken(42, hashA[:])
	if err != nil {
		t.Fatalf("issue inbox token: %v", err)
	}
	otpTok, err := cipher.OTPToken(42, hashA[:])
	if err != nil {
		t.Fatalf("issue otp token: %v", err)
	}
	if inboxTok == otpTok {
		t.Fatal("inbox token collided with OTP token")
	}

	parsedID, ok := InboxTokenAliasID(inboxTok)
	if !ok || parsedID != 42 {
		t.Fatalf("InboxTokenAliasID = (%d, %v), want (42, true)", parsedID, ok)
	}
	if !cipher.VerifyInboxToken(inboxTok, 42, hashA[:]) {
		t.Fatal("valid inbox token failed verification")
	}
	if cipher.VerifyOTPToken(inboxTok, 42, hashA[:]) || cipher.VerifyInboxToken(otpTok, 42, hashA[:]) {
		t.Fatal("cross-purpose token verification succeeded")
	}
	if cipher.VerifyInboxToken(inboxTok, 42, hashB[:]) {
		t.Fatal("inbox token survived API key rotation")
	}

	projTok, err := cipher.ProjectOTPToken(42, hashA[:], "client-chatgpt-01")
	if err != nil {
		t.Fatalf("issue project otp token: %v", err)
	}
	if !strings.HasPrefix(projTok, "icp_") {
		t.Fatalf("project otp token prefix = %q, want icp_", projTok)
	}
	projAliasID, projClientID, ok := ProjectOTPTokenIdentity(projTok)
	if !ok || projAliasID != 42 || projClientID != "client-chatgpt-01" {
		t.Fatalf("ProjectOTPTokenIdentity = (%d, %q, %v)", projAliasID, projClientID, ok)
	}
	if !cipher.VerifyProjectOTPToken(projTok, 42, hashA[:], "client-chatgpt-01") {
		t.Fatal("valid project otp token failed verification")
	}
	if cipher.VerifyProjectOTPToken(projTok, 42, hashA[:], "client-deepseek-02") {
		t.Fatal("project otp token verified for different client ID")
	}
	if cipher.VerifyProjectOTPToken(projTok, 43, hashA[:], "client-chatgpt-01") {
		t.Fatal("project otp token verified for different alias ID")
	}
	if cipher.VerifyProjectOTPToken(projTok, 42, hashB[:], "client-chatgpt-01") {
		t.Fatal("project otp token survived API key rotation")
	}
}
