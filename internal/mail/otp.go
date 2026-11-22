package mail

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"

	"icloud-api/internal/domain"
)

const (
	maxOTPHTMLInputBytes  = 2 << 20
	maxOTPPlainTextBytes  = 512 << 10
	otpKeywordWindowBytes = 160
)

var otpKeywords = []string{
	"verification code", "security code", "one-time", "one time", "otp", "verify", "code",
	"passcode", "sign-in code", "signin code", "access code", "authorization code", "auth code",
	"验证码", "校验码", "动态码", "安全码", "一次性密码", "一次性验证码", "确认码", "动态密码", "身份验证", "登录验证",
	"驗證碼", "驗證代碼", "安全代碼", "確認碼", "一次性驗證碼",
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
	sources := []string{subject, maskURLsForOTP(textBody)}
	if strings.TrimSpace(htmlBody) != "" {
		plain := maskURLsForOTP(readableHTMLText(htmlBody))
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
			if isLikelyNonOTPToken(source, start, index) {
				continue
			}
			windowStart := alignUTF8Start(source, max(0, start-otpKeywordWindowBytes))
			windowEnd := alignUTF8End(source, min(len(source), index+otpKeywordWindowBytes))
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

func alignUTF8Start(s string, pos int) int {
	for pos > 0 && pos < len(s) && !utf8.RuneStart(s[pos]) {
		pos--
	}
	return pos
}

func alignUTF8End(s string, pos int) int {
	for pos < len(s) && !utf8.RuneStart(s[pos]) {
		pos++
	}
	return pos
}

// isLikelyNonOTPToken filters CSS hex colors (#123456) and currency amounts
// ($123456.00) while preserving standalone 6-digit codes.
func isLikelyNonOTPToken(source string, start, end int) bool {
	if start > 0 {
		prev, _ := utf8.DecodeLastRuneInString(source[:start])
		switch prev {
		case '$', '¥', '€', '£', '₹':
			return true
		case '#':
			// Allow "code #123456" only when preceded by an OTP keyword nearby,
			// otherwise reject CSS color literals such as "color:#123456" or "color: #123456".
			prefixStart := max(0, start-24)
			prefix := strings.ToLower(source[prefixStart:start])
			if strings.Contains(prefix, "color") || strings.Contains(prefix, "background") || strings.Contains(prefix, "border") {
				return true
			}
		}
	}
	if end+2 < len(source) && source[end] == '.' && source[end+1] >= '0' && source[end+1] <= '9' && source[end+2] >= '0' && source[end+2] <= '9' {
		// Decimal currency/amount like 123456.00
		return true
	}
	return false
}

// maskURLsForOTP replaces URL bodies with spaces so 6-digit path segments or
// tracking query parameters inside plain-text links do not outscore real OTPs.
func maskURLsForOTP(input string) string {
	if !strings.Contains(input, "://") {
		return input
	}
	var b strings.Builder
	b.Grow(len(input))
	i := 0
	for i < len(input) {
		if (i+7 <= len(input) && strings.EqualFold(input[i:i+7], "http://")) ||
			(i+8 <= len(input) && strings.EqualFold(input[i:i+8], "https://")) {
			b.WriteByte(' ')
			for i < len(input) {
				r, size := utf8.DecodeRuneInString(input[i:])
				if size < 1 {
					size = 1
				}
				if unicode.IsSpace(r) || r == '"' || r == '\'' || r == '<' || r == '>' || r == ')' || r == ']' {
					break
				}
				i += size
			}
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(input[i])
		i++
	}
	return b.String()
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
			if isShortASCIIKeyword(keyword) && adjacentLetterOrNumber(window, start, end) {
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

func isShortASCIIKeyword(keyword string) bool {
	for i := 0; i < len(keyword); i++ {
		if keyword[i] >= 0x80 {
			return false
		}
	}
	return true
}

func readableHTMLText(source string) string {
	if len(source) > maxOTPHTMLInputBytes {
		source = source[:maxOTPHTMLInputBytes]
	}
	tokenizer := xhtml.NewTokenizer(strings.NewReader(source))
	tokenizer.SetMaxBuf(len(source) + 1)

	var out strings.Builder
	out.Grow(min(len(source), 32<<10))
	pendingSpace := false
	var skipTag string
	skipDepth := 0

	appendSpace := func() {
		if out.Len() > 0 {
			pendingSpace = true
		}
	}
	appendText := func(raw string) bool {
		unescaped := html.UnescapeString(raw)
		for _, r := range unescaped {
			if unicode.IsSpace(r) {
				appendSpace()
				continue
			}
			if pendingSpace && out.Len() > 0 {
				if out.Len()+1 >= maxOTPPlainTextBytes {
					return false
				}
				out.WriteByte(' ')
				pendingSpace = false
			}
			rLen := utf8.RuneLen(r)
			if rLen < 0 || out.Len()+rLen > maxOTPPlainTextBytes {
				return false
			}
			out.WriteRune(r)
		}
		return true
	}

	for {
		typeValue := tokenizer.Next()
		switch typeValue {
		case xhtml.ErrorToken:
			return out.String()
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			name, hasAttr := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			if tag == "noscript" {
				tokenizer.NextIsNotRawText()
			}
			if skipDepth > 0 {
				if skipTag == "head" && tag == "body" {
					skipTag = ""
					skipDepth = 0
				} else {
					if tag == skipTag && !isOTPHTMLVoidTag(tag) && typeValue != xhtml.SelfClosingTagToken {
						skipDepth++
					}
					continue
				}
			}
			if isOTPHTMLIgnoredTag(tag) || otpElementHidden(tokenizer, hasAttr) {
				if !isOTPHTMLVoidTag(tag) && typeValue != xhtml.SelfClosingTagToken {
					skipTag = tag
					skipDepth = 1
				}
				continue
			}
			if tag == "br" || tag == "td" || tag == "th" || isOTPHTMLBlockTag(tag) {
				appendSpace()
			}
		case xhtml.EndTagToken:
			name, _ := tokenizer.TagName()
			tag := strings.ToLower(string(name))
			if skipDepth > 0 {
				if tag == skipTag {
					skipDepth--
					if skipDepth == 0 {
						skipTag = ""
					}
				}
				continue
			}
			if tag == "td" || tag == "th" || isOTPHTMLBlockTag(tag) {
				appendSpace()
			}
		case xhtml.TextToken:
			if skipDepth == 0 {
				if !appendText(string(tokenizer.Text())) {
					return out.String()
				}
			}
		}
	}
}

func otpElementHidden(tokenizer *xhtml.Tokenizer, hasAttr bool) bool {
	hidden := false
	for hasAttr {
		k, v, more := tokenizer.TagAttr()
		hasAttr = more
		switch strings.ToLower(string(k)) {
		case "hidden":
			hidden = true
		case "aria-hidden":
			if strings.EqualFold(strings.TrimSpace(string(v)), "true") {
				hidden = true
			}
		case "style":
			if otpInlineStyleHidden(string(v)) {
				hidden = true
			}
		}
	}
	return hidden
}

func otpInlineStyleHidden(style string) bool {
	for decl := range strings.SplitSeq(style, ";") {
		prop, val, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		prop = strings.ToLower(strings.TrimSpace(prop))
		val = strings.ToLower(strings.TrimSpace(val))
		val = strings.TrimSpace(strings.TrimSuffix(val, "!important"))
		switch prop {
		case "display":
			if val == "none" {
				return true
			}
		case "visibility":
			if val == "hidden" || val == "collapse" {
				return true
			}
		case "opacity":
			if val == "0" {
				return true
			}
		case "font-size":
			if val == "0" || val == "0px" || val == "0pt" || val == "0em" || val == "0rem" || val == "0%" {
				return true
			}
		case "mso-hide":
			if val == "all" {
				return true
			}
		}
	}
	return false
}

func isOTPHTMLIgnoredTag(tag string) bool {
	switch tag {
	case "script", "style", "head", "title", "template", "svg", "canvas", "iframe", "noembed", "noframes", "textarea":
		return true
	default:
		return false
	}
}

func isOTPHTMLVoidTag(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func isOTPHTMLBlockTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "body", "caption", "center", "dd",
		"details", "dialog", "dir", "div", "dl", "dt", "fieldset", "figcaption", "figure",
		"footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr",
		"html", "legend", "li", "main", "menu", "nav", "ol", "optgroup", "option", "p",
		"pre", "section", "summary", "table", "tbody", "tfoot", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}
