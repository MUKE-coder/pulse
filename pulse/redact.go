package pulse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Redaction helpers shared by error capture (request headers, query string,
// body) and dependency tracking (outbound URLs).
//
// Everything here fails closed: when Pulse cannot confidently strip secrets
// from a value, it stores a size marker instead of the value.

const redactedPlaceholder = "[REDACTED]"

// truncatedMarker is appended to a captured JSON body that ended before the
// document did — either cut off at Errors.MaxBodySize or malformed. Nothing
// after the cut-off point is emitted.
const truncatedMarker = " …[truncated]"

// sensitiveHeaders are header names (lowercase) whose values are redacted in
// captured request context.
var sensitiveHeaders = map[string]bool{
	"authorization":        true,
	"cookie":               true,
	"set-cookie":           true,
	"x-api-key":            true,
	"x-auth-token":         true,
	"x-access-token":       true,
	"x-refresh-token":      true,
	"x-csrf-token":         true,
	"x-xsrf-token":         true,
	"x-amz-security-token": true,
	"x-goog-api-key":       true,
	"proxy-authorization":  true,
}

// sensitiveFieldNames are matched exactly against a normalized field name
// (see normalizeFieldName), so "cc_number", "ccNumber" and "cc-number" all
// match "ccnumber". Short or ambiguous names belong here rather than in
// sensitiveFieldStems.
var sensitiveFieldNames = map[string]bool{
	"pass":          true,
	"pin":           true,
	"auth":          true,
	"authorization": true,
	"ssn":           true,
	"cvc":           true,
	"cvv":           true,
	"ccnumber":      true,
	"otp":           true,
	"sessionid":     true,
}

// sensitiveFieldStems are matched as substrings of a normalized field name,
// so "newPassword", "stripe_token" and "X-Amz-Signature" are all caught.
// Only long, high-signal stems belong here — short ones like "pin" or "ssn"
// would match unrelated fields ("spinner", "classname").
var sensitiveFieldStems = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"apikey",
	"credential",
	"privatekey",
	"cardnumber",
	"signature",
}

// isSensitiveField reports whether values of the named body field, form key
// or query parameter should be redacted.
func isSensitiveField(name string) bool {
	n := normalizeFieldName(name)
	if sensitiveFieldNames[n] {
		return true
	}
	for _, stem := range sensitiveFieldStems {
		if strings.Contains(n, stem) {
			return true
		}
	}
	return false
}

// normalizeFieldName lowercases name and drops '_', '-' and '.', so naming
// conventions (snake_case, camelCase, kebab-case) don't defeat matching.
func normalizeFieldName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' || c == '-' || c == '.':
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// mediaType returns the lowercased media type of a Content-Type header value,
// without parameters ("application/json; charset=utf-8" → "application/json").
func mediaType(contentType string) string {
	ct := strings.ToLower(contentType)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// redactableContentType reports whether Pulse knows how to redact a body of
// this content type. A missing Content-Type counts, because many clients send
// JSON without one; redactBody sniffs such bodies.
func redactableContentType(contentType string) bool {
	switch ct := mediaType(contentType); {
	case ct == "", ct == "application/json", strings.HasSuffix(ct, "+json"),
		ct == "application/x-www-form-urlencoded":
		return true
	}
	return false
}

// redactBody returns a copy of body that is safe to store:
//
//   - JSON (application/json, */*+json, or no Content-Type but JSON-shaped):
//     the value of every sensitive key is replaced, at any depth. Works on
//     truncated and malformed documents — see redactJSONBody.
//   - application/x-www-form-urlencoded: sensitive keys are replaced.
//   - anything else: replaced wholesale by a size marker.
func redactBody(contentType string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	switch ct := mediaType(contentType); {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		return redactJSONBody(body)
	case ct == "application/x-www-form-urlencoded":
		return []byte(redactQuery(string(body)))
	case ct == "" && looksLikeJSON(body):
		return redactJSONBody(body)
	}
	return []byte(omittedBodyMarker(int64(len(body)), contentType))
}

// omittedBodyMarker is stored in place of a body Pulse cannot redact.
func omittedBodyMarker(size int64, contentType string) string {
	ct := mediaType(contentType)
	if ct == "" {
		ct = "unknown content type"
	}
	return fmt.Sprintf("[body omitted: %d bytes of %s]", size, ct)
}

func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// redactJSONBody re-emits body token by token, replacing the value of every
// sensitive key — scalar or whole object/array — with the placeholder.
//
// Streaming (rather than decode-then-marshal) keeps the original key order
// and, crucially, still works on a body cut off at MaxBodySize or otherwise
// malformed: everything before the problem is emitted redacted, then
// truncatedMarker, and nothing after it.
func redactJSONBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	type frame struct {
		object  bool
		wantKey bool // object only: the next token is a key
		n       int  // members emitted so far, for comma placement
	}
	var (
		out        bytes.Buffer
		stack      []frame
		skipDepth  int  // >0 while discarding a redacted object/array
		redactNext bool // the next value belongs to a sensitive key
	)
	// valueDone records that a complete value was emitted in the current
	// container.
	valueDone := func() {
		if len(stack) == 0 {
			return
		}
		top := &stack[len(stack)-1]
		top.n++
		top.wantKey = top.object
	}

	for {
		tok, err := dec.Token()
		if err != nil {
			if len(stack) > 0 || skipDepth > 0 || err != io.EOF {
				out.WriteString(truncatedMarker)
			}
			return out.Bytes()
		}

		if skipDepth > 0 {
			if d, ok := tok.(json.Delim); ok {
				if d == '{' || d == '[' {
					skipDepth++
				} else {
					skipDepth--
				}
				if skipDepth == 0 {
					valueDone()
				}
			}
			continue
		}

		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			out.WriteByte(byte(d))
			stack = stack[:len(stack)-1]
			valueDone()
			continue
		}

		if len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.n > 0 && (top.wantKey || !top.object) {
				out.WriteByte(',')
			}
			if top.wantKey {
				key, _ := tok.(string)
				writeJSONString(&out, key)
				out.WriteByte(':')
				top.wantKey = false
				redactNext = isSensitiveField(key)
				continue
			}
		}

		if redactNext {
			redactNext = false
			writeJSONString(&out, redactedPlaceholder)
			if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
				skipDepth = 1
			} else {
				valueDone()
			}
			continue
		}

		switch t := tok.(type) {
		case json.Delim: // '{' or '['; closers are handled above
			out.WriteByte(byte(t))
			stack = append(stack, frame{object: t == '{', wantKey: t == '{'})
		case string:
			writeJSONString(&out, t)
			valueDone()
		case json.Number:
			out.WriteString(t.String())
			valueDone()
		case bool:
			out.WriteString(strconv.FormatBool(t))
			valueDone()
		case nil:
			out.WriteString("null")
			valueDone()
		}
	}
}

// writeJSONString appends s as a JSON string literal, without the HTML
// escaping json.Marshal applies.
func writeJSONString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)       // a string always encodes
	b.Truncate(b.Len() - 1) // drop Encode's trailing newline
}

// redactQuery redacts the values of sensitive keys in a URL query string or
// form-encoded body. Pair order and the original encoding of everything it
// leaves alone are preserved. Pairs are handled independently, so a
// malformed or truncated pair never causes the rest to be kept raw.
func redactQuery(raw string) string {
	if raw == "" {
		return raw
	}
	pairs := strings.Split(raw, "&")
	changed := false
	for i, pair := range pairs {
		key, _, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		if isSensitiveField(name) {
			pairs[i] = key + "=" + redactedPlaceholder
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return strings.Join(pairs, "&")
}

// redactURL renders u for storage with sensitive query parameters redacted
// and any userinfo password masked.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	cp.RawQuery = redactQuery(cp.RawQuery)
	return cp.Redacted()
}
