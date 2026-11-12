package mail

import (
	"bytes"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"testing"
)

func TestExtractOTPMultilingualVerification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, label string }{
		{"Chinese", "验证码"},
		{"traditional Chinese", "驗證碼"},
		{"English", "Your verification code"},
		{"Vietnamese", "Mã xác minh của bạn"},
		{"Spanish", "Tu código de verificación"},
		{"Portuguese", "Seu código de verificação"},
		{"French", "Votre code de vérification"},
		{"German", "Dein Bestätigungscode"},
		{"Italian", "Il tuo codice di verifica"},
		{"Indonesian", "Kode verifikasi Anda"},
		{"Turkish", "Doğrulama kodu"},
		{"Japanese", "認証コード"},
		{"Korean", "인증 코드"},
		{"Arabic", "رمز التحقق"},
		{"Russian", "Код подтверждения"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Both unrelated numeric references are valid six-digit strings.
			body := "Reference: 111111. " + test.label + ": 246810. Reference: 999999."
			if got := ExtractOTP("Meta", body, ""); got != "246810" {
				t.Fatalf("plain text selected %q", got)
			}
			html := `<head><title>verification code 777777</title></head>` +
				`<p>Reference: 111111.</p><p>` + test.label + `: <b>246810</b></p>` +
				`<a href="https://example.invalid/888888">Help</a><p>Reference: 999999.</p>` +
				`<script>verification code 666666</script>`
			if got := ExtractOTP("Meta", "", html); got != "246810" {
				t.Fatalf("HTML selected %q", got)
			}
		})
	}
}

func TestExtractOTPUnicodeCaseOffsets(t *testing.T) {
	t.Parallel()
	// U+0130 occupies two UTF-8 bytes but lowercases to one ASCII byte.
	text := "İİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİİ Verification code: 246810"
	if got := ExtractOTP("", text, ""); got != "246810" {
		t.Fatalf("selected %q", got)
	}
}

func TestExtractOTPVerificationLabelAssociation(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"Reference 111111 verification code: 246810",
		"246810 is your verification code. Reference: 999999.",
		"246810 is your code. Barcode: 999999.",
		"Reference: 111111. Mã xác minh: 246810. Reference: 999999.",
		"認証コード: 246810。参照番号: 999999。",
	} {
		if got := ExtractOTP("", text, ""); got != "246810" {
			t.Fatalf("label association selected %q", got)
		}
	}
}

func TestMultilingualOTPSurvivesMIMEDecoding(t *testing.T) {
	t.Parallel()
	for _, contentType := range []string{"text/plain", "text/html"} {
		t.Run(contentType, func(t *testing.T) {
			var encoded bytes.Buffer
			writer := quotedprintable.NewWriter(&encoded)
			body := "Reference: 111111. Mã xác minh: 246810."
			if contentType == "text/html" {
				body = "<p>Reference: 111111.</p><p>Mã xác minh: <b>246810</b></p>"
			}
			if _, err := writer.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			raw := fmt.Sprintf("From: Meta <notification@example.invalid>\r\nTo: recipient@example.invalid\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s",
				mime.BEncoding.Encode("UTF-8", "Xác minh email Meta của bạn"), contentType, encoded.String())
			parsed, err := parseMIMEMessage([]byte(raw), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if got := ExtractOTP(parsed.subject, parsed.textBody, parsed.htmlBody); got != "246810" {
				t.Fatalf("decoded MIME selected %q", got)
			}
		})
	}
}
