package mail

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"

	"icloud-api/internal/domain"
)

var otpKeywords = []string{
	"verification code", "security code", "one-time", "one time", "otp", "verify", "code",
	"验证码", "校验码", "动态码", "安全码", "一次性密码",
	"驗證碼", "驗證代碼", "安全代碼",
	"verification number", "confirmation code", "authentication code", "login code",
	"mã xác minh", "mã xác nhận", "mã bảo mật",
	"código de verificación", "código de seguridad", "código de confirmación",
	"código de verificação", "código de segurança", "código de confirmação",
	"code de vérification", "code de confirmation", "code de sécurité",
	"bestätigungscode", "sicherheitscode", "anmeldecode",
	"codice di verifica", "codice di sicurezza", "codice di conferma",
	"kode verifikasi", "kode konfirmasi", "kode keamanan",
	"doğrulama kodu", "güvenlik kodu", "onay kodu",
	"認証コード", "確認コード", "セキュリティコード", "인증 코드", "보안 코드",
	"رمز التحقق", "رمز التأكيد", "رمز الأمان",
	"код подтверждения", "код безопасности", "код входа",
}

type otpCandidate struct {
	value string
	score int
	order int
}

// ExtractOTP returns one bounded numeric code. A candidate must contain exactly
// six ASCII digits and must not touch another letter or number.
func ExtractOTP(subject, textBody, htmlBody string) string {
	sources := []string{subject, textBody}
	if strings.TrimSpace(htmlBody) != "" {
		plain := readableHTMLText(htmlBody)
		sources = append(sources, plain)
	}
	best := otpCandidate{score: -1}
	order := 0
	for sourceIndex, source := range sources {
		// Scan the same folded text used for keyword offsets: case folding can
		// change UTF-8 byte lengths (for example Turkish capital dotted I).
		source = strings.ToLower(source)
		for index := 0; index < len(source); {
			if source[index] < '0' || source[index] > '9' {
				_, size := utf8.DecodeRuneInString(source[index:])
				if size < 1 {
					size = 1
				}
				index += size
				continue
			}
			start := index
			for index < len(source) && source[index] >= '0' && source[index] <= '9' {
				index++
			}
			if !domain.IsSixDigitOTP(source[start:index]) || adjacentLetterOrNumber(source, start, index) {
				continue
			}
			windowStart := start - 64
			if windowStart < 0 {
				windowStart = 0
			}
			windowEnd := index + 64
			if windowEnd > len(source) {
				windowEnd = len(source)
			}
			score := 100 - sourceIndex
			window := source[windowStart:windowEnd]
			if distance, found := nearestOTPKeyword(window, start-windowStart, index-windowStart); found {
				score += 10_000 - min(distance, 9_000)
			}
			candidate := otpCandidate{value: source[start:index], score: score, order: order}
			order++
			if candidate.score > best.score || candidate.score == best.score && candidate.order < best.order {
				best = candidate
			}
		}
	}
	return best.value
}

func adjacentLetterOrNumber(source string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(source[:start])
		if unicode.IsLetter(previous) || unicode.IsNumber(previous) {
			return true
		}
	}
	if end < len(source) {
		next, _ := utf8.DecodeRuneInString(source[end:])
		if unicode.IsLetter(next) || unicode.IsNumber(next) {
			return true
		}
	}
	return false
}

func nearestOTPKeyword(window string, candidateStart, candidateEnd int) (int, bool) {
	best := 0
	found := false
	for _, keyword := range otpKeywords {
		for offset := 0; offset <= len(window); {
			index := strings.Index(window[offset:], keyword)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(keyword)
			offset = start + 1
			if (keyword == "code" || keyword == "verify" || keyword == "otp") && adjacentLetterOrNumber(window, start, end) {
				continue
			}
			distance := 0
			switch {
			case end < candidateStart:
				if strings.ContainsAny(window[end:candidateStart], ".;!?。；！？") {
					continue
				}
				distance = candidateStart - end
			case start > candidateEnd:
				if strings.ContainsAny(window[candidateEnd:start], ".;!?。；！？") {
					continue
				}
				// Labels normally introduce a code. A reference number just
				// before the label must not outrank the number it introduces.
				// Codes preceding their label remain eligible as a fallback.
				distance = start - candidateEnd + 64
			}
			if !found || distance < best {
				best, found = distance, true
			}
		}
	}
	return best, found
}

func readableHTMLText(source string) string {
	tokenizer := xhtml.NewTokenizer(strings.NewReader(source))
	var result strings.Builder
	ignoredDepth := 0
	for {
		typeValue := tokenizer.Next()
		switch typeValue {
		case xhtml.ErrorToken:
			return strings.Join(strings.Fields(html.UnescapeString(result.String())), " ")
		case xhtml.StartTagToken:
			name, _ := tokenizer.TagName()
			switch strings.ToLower(string(name)) {
			case "script", "style", "head", "template", "svg", "canvas":
				ignoredDepth++
			}
		case xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			switch strings.ToLower(string(name)) {
			case "script", "style", "head", "template", "svg", "canvas":
				if ignoredDepth > 0 {
					ignoredDepth--
				}
			}
		case xhtml.TextToken:
			if ignoredDepth == 0 {
				result.WriteByte(' ')
				result.Write(tokenizer.Text())
			}
		}
	}
}
