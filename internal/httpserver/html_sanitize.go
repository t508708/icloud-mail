package httpserver

import (
	"html"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

const maxSanitizedHTMLBytes = 256 << 10

type sanitizedEmailHTML struct {
	HTML                string
	RemoteImagesBlocked bool
}

// sanitizeEmailHTML converts untrusted email HTML into a safe, script-free HTML
// fragment suitable for rendering inside a sandboxed iframe (`sandbox=""`).
func sanitizeEmailHTML(raw string, allowRemoteImages bool) sanitizedEmailHTML {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sanitizedEmailHTML{}
	}
	if len(raw) > maxSanitizedHTMLBytes*2 {
		raw = raw[:maxSanitizedHTMLBytes*2]
	}

	tokenizer := xhtml.NewTokenizer(strings.NewReader(raw))
	tokenizer.SetMaxBuf(len(raw) + 1)

	var out strings.Builder
	out.Grow(min(len(raw), 32<<10))

	var openStack []string
	var skipTag string
	skipDepth := 0
	remoteBlocked := false

	for out.Len() < maxSanitizedHTMLBytes {
		tt := tokenizer.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		switch tt {
		case xhtml.TextToken:
			if skipDepth > 0 {
				continue
			}
			escaped := html.EscapeString(string(tokenizer.Text()))
			if out.Len()+len(escaped) > maxSanitizedHTMLBytes {
				remaining := maxSanitizedHTMLBytes - out.Len()
				if remaining > 0 {
					out.WriteString(html.EscapeString(truncateValidUTF8(string(tokenizer.Text()), remaining)))
				}
				break
			}
			out.WriteString(escaped)

		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			rawName, hasAttr := tokenizer.TagName()
			tag := strings.ToLower(string(rawName))
			if tag == "noscript" {
				tokenizer.NextIsNotRawText()
			}
			if skipDepth > 0 {
				if tag == skipTag && !isVoidHTMLElement(tag) && tt != xhtml.SelfClosingTagToken {
					skipDepth++
				}
				continue
			}
			if isBlockedEmailHTMLTag(tag) {
				if !isVoidHTMLElement(tag) && tt != xhtml.SelfClosingTagToken {
					skipTag = tag
					skipDepth = 1
				}
				continue
			}
			if !isAllowedEmailHTMLTag(tag) {
				// Unwrap unknown/layout wrapper tags while preserving safe inner text.
				continue
			}

			attrs, blockedImg, skipImg := sanitizeEmailAttributes(tag, tokenizer, hasAttr, allowRemoteImages)
			if blockedImg {
				remoteBlocked = true
			}
			if skipImg {
				continue
			}

			out.WriteByte('<')
			out.WriteString(tag)
			for _, attr := range attrs {
				out.WriteByte(' ')
				out.WriteString(attr[0])
				out.WriteString(`="`)
				out.WriteString(html.EscapeString(attr[1]))
				out.WriteByte('"')
			}
			if isVoidHTMLElement(tag) {
				out.WriteByte('>')
			} else {
				out.WriteByte('>')
				if tt != xhtml.SelfClosingTagToken {
					openStack = append(openStack, tag)
				} else {
					out.WriteString("</")
					out.WriteString(tag)
					out.WriteByte('>')
				}
			}

		case xhtml.EndTagToken:
			rawName, _ := tokenizer.TagName()
			tag := strings.ToLower(string(rawName))
			if skipDepth > 0 {
				if tag == skipTag {
					skipDepth--
					if skipDepth == 0 {
						skipTag = ""
					}
				}
				continue
			}
			if !isAllowedEmailHTMLTag(tag) || isVoidHTMLElement(tag) {
				continue
			}
			// Pop matching tag cleanly to keep output balanced.
			matchIdx := -1
			for idx := len(openStack) - 1; idx >= 0; idx-- {
				if openStack[idx] == tag {
					matchIdx = idx
					break
				}
			}
			if matchIdx >= 0 {
				for idx := len(openStack) - 1; idx >= matchIdx; idx-- {
					out.WriteString("</")
					out.WriteString(openStack[idx])
					out.WriteByte('>')
				}
				openStack = openStack[:matchIdx]
			}
		}
	}

	for idx := len(openStack) - 1; idx >= 0; idx-- {
		out.WriteString("</")
		out.WriteString(openStack[idx])
		out.WriteByte('>')
	}

	return sanitizedEmailHTML{
		HTML:                out.String(),
		RemoteImagesBlocked: remoteBlocked,
	}
}

func isBlockedEmailHTMLTag(tag string) bool {
	switch tag {
	case "script", "style", "head", "title", "iframe", "frame", "frameset",
		"object", "embed", "applet", "form", "input", "button", "select",
		"option", "optgroup", "textarea", "base", "meta", "link", "template",
		"svg", "math", "canvas", "audio", "video", "source", "track", "portal",
		"noembed", "noframes", "plaintext", "xmp":
		return true
	default:
		return false
	}
}

func isAllowedEmailHTMLTag(tag string) bool {
	switch tag {
	case "div", "span", "p", "br", "hr", "a", "img",
		"b", "strong", "i", "em", "u", "s", "del", "ins", "mark", "small", "sub", "sup", "code", "pre", "blockquote",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "dl", "dt", "dd",
		"table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "colgroup", "col",
		"center", "font", "section", "article", "header", "footer", "main", "aside", "figure", "figcaption", "details", "summary":
		return true
	default:
		return false
	}
}

func sanitizeEmailAttributes(
	tag string,
	tokenizer *xhtml.Tokenizer,
	hasAttr bool,
	allowRemoteImages bool,
) (attrs [][2]string, remoteBlocked bool, dropElement bool) {
	hasHref := false
	for hasAttr {
		rawKey, rawVal, more := tokenizer.TagAttr()
		hasAttr = more
		key := strings.ToLower(strings.TrimSpace(string(rawKey)))
		val := strings.TrimSpace(string(rawVal))
		if key == "" || strings.HasPrefix(key, "on") || key == "srcdoc" || key == "formaction" || strings.Contains(key, ":") {
			continue
		}
		switch key {
		case "style":
			if safeStyle := sanitizeInlineCSS(val); safeStyle != "" {
				attrs = append(attrs, [2]string{"style", safeStyle})
			}
		case "href":
			if tag == "a" {
				if safeURL, ok := sanitizeSafeLinkURL(val); ok {
					attrs = append(attrs, [2]string{"href", safeURL})
					hasHref = true
				}
			}
		case "src":
			if tag == "img" {
				safeSrc, isRemote, ok := sanitizeImageSrc(val)
				if !ok {
					return nil, false, true
				}
				if isRemote && !allowRemoteImages {
					return nil, true, true
				}
				attrs = append(attrs, [2]string{"src", safeSrc})
				attrs = append(attrs, [2]string{"loading", "lazy"})
				attrs = append(attrs, [2]string{"referrerpolicy", "no-referrer"})
			}
		case "alt", "title":
			if len(val) <= 512 {
				attrs = append(attrs, [2]string{key, val})
			}
		case "width", "height", "align", "valign", "colspan", "rowspan", "cellpadding", "cellspacing", "border", "bgcolor", "color", "dir", "lang":
			if len(val) <= 64 && !strings.ContainsAny(val, "\"'<>`()") {
				attrs = append(attrs, [2]string{key, val})
			}
		}
	}
	if tag == "a" && hasHref {
		attrs = append(attrs, [2]string{"target", "_blank"})
		attrs = append(attrs, [2]string{"rel", "noopener noreferrer nofollow"})
	}
	return attrs, remoteBlocked, false
}

func sanitizeSafeLinkURL(raw string) (string, bool) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" || len(cleaned) > 2048 {
		return "", false
	}
	parsed, err := url.Parse(cleaned)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "mailto":
		return parsed.String(), true
	default:
		return "", false
	}
}

func sanitizeImageSrc(raw string) (safe string, remote bool, ok bool) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" || len(cleaned) > 4096 {
		return "", false, false
	}
	lower := strings.ToLower(cleaned)
	if strings.HasPrefix(lower, "data:image/") {
		// Allow safe raster inline images (not SVG which can carry scripts/styles).
		if strings.HasPrefix(lower, "data:image/png;") ||
			strings.HasPrefix(lower, "data:image/jpeg;") ||
			strings.HasPrefix(lower, "data:image/jpg;") ||
			strings.HasPrefix(lower, "data:image/gif;") ||
			strings.HasPrefix(lower, "data:image/webp;") {
			return cleaned, false, true
		}
		return "", false, false
	}
	parsed, err := url.Parse(cleaned)
	if err != nil {
		return "", false, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "https" {
		return parsed.String(), true, true
	}
	return "", false, false
}

func sanitizeInlineCSS(raw string) string {
	if len(raw) > 4096 {
		raw = raw[:4096]
	}
	var kept []string
	for decl := range strings.SplitSeq(raw, ";") {
		prop, val, found := strings.Cut(decl, ":")
		if !found {
			continue
		}
		prop = strings.ToLower(strings.TrimSpace(prop))
		val = strings.TrimSpace(val)
		lowerVal := strings.ToLower(val)
		if prop == "" || val == "" || len(val) > 256 {
			continue
		}
		if strings.Contains(lowerVal, "url(") ||
			strings.Contains(lowerVal, "expression") ||
			strings.Contains(lowerVal, "javascript:") ||
			strings.Contains(lowerVal, "vbscript:") ||
			strings.Contains(lowerVal, "data:") ||
			strings.Contains(lowerVal, "behavior") ||
			strings.Contains(lowerVal, "-moz-binding") ||
			strings.Contains(lowerVal, "@import") {
			continue
		}
		if isAllowedCSSProperty(prop) {
			kept = append(kept, prop+": "+val)
		}
	}
	return strings.Join(kept, "; ")
}

func isAllowedCSSProperty(prop string) bool {
	switch prop {
	case "color", "background", "background-color",
		"font", "font-family", "font-size", "font-style", "font-weight", "line-height", "letter-spacing",
		"text-align", "text-decoration", "text-transform", "text-indent", "white-space", "word-break", "overflow-wrap",
		"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
		"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
		"border", "border-top", "border-right", "border-bottom", "border-left",
		"border-color", "border-style", "border-width", "border-radius", "border-collapse", "border-spacing",
		"width", "max-width", "min-width", "height", "max-height", "min-height",
		"display", "vertical-align", "list-style", "list-style-type":
		return true
	default:
		return false
	}
}

func truncateValidUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && (s[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return s[:maxBytes]
}
