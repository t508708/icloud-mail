package mail

import (
	"strings"
	"testing"

	"icloud-api/internal/domain"
)

func TestExtractOTPDefeatsHiddenBaitSplitTagsAndNoiseNumbers(t *testing.T) {
	t.Run("hidden honeypot ignored in favor of visible code", func(t *testing.T) {
		html := `<html><body>
			<div style="display:none">Your verification code is 999888</div>
			<span style="font-size:0;line-height:0">验证码 777666</span>
			<p>欢迎登录，您的动态安全验证码为 <strong>482910</strong>，5 分钟内有效。</p>
		</body></html>`
		if got := ExtractOTP("登录安全确认", "", html); got != "482910" {
			t.Fatalf("ExtractOTP = %q, want 482910", got)
		}
	})

	t.Run("adjacent inline spans do not glue two 3-digit numbers", func(t *testing.T) {
		html := `<div><span>123</span><span>456</span></div><p>您的验证码是 650321</p>`
		if got := ExtractOTP("账号验证", "", html); got != "650321" {
			t.Fatalf("ExtractOTP = %q, want 650321", got)
		}
	})

	t.Run("URLs hex colors and prices are masked out", func(t *testing.T) {
		text := "Order # color #345678 amount $123456.00 tracking https://example.com/item/890123\nYour login code: 591827"
		if got := ExtractOTP("Order & Login", text, ""); got != "591827" {
			t.Fatalf("ExtractOTP = %q, want 591827", got)
		}
	})
}

func TestParseMIMEPreviewAndExtractPlusTags(t *testing.T) {
	raw := strings.Join([]string{
		"From: OpenAI <noreply@tm.openai.com>",
		"To: user+chatgpt@icloud.com, other@icloud.com",
		"Cc: user+Backup@icloud.com",
		"Subject: Your ChatGPT code is 314159",
		"Message-ID: <preview-test@openai.com>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"b1\"",
		"",
		"--b1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Your ChatGPT verification code is 314159.",
		"--b1",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>Your ChatGPT verification code is <b>314159</b>.</p>",
		"--b1--",
		"",
	}, "\r\n")

	preview, err := ParseMIMEPreview(strings.NewReader(raw), int64(len(raw)), 64<<10)
	if err != nil {
		t.Fatalf("ParseMIMEPreview: %v", err)
	}
	if preview.ExtractedOTP != "314159" || !strings.Contains(preview.HTMLBody, "<b>314159</b>") {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	tags := ExtractPlusTags("user@icloud.com", preview.To, preview.CC)
	if len(tags) != 2 || tags[0] != "chatgpt" || tags[1] != "backup" {
		t.Fatalf("ExtractPlusTags = %v, want [chatgpt backup]", tags)
	}
	if empty := ExtractPlusTags("user+explicit@icloud.com", []domain.MailAddress{{Email: "user+explicit@icloud.com"}}); len(empty) != 0 {
		t.Fatalf("explicit + alias should not emit sub-tags: %v", empty)
	}
}
