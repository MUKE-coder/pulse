package pulse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Redaction of secrets from what Pulse stores: request headers, query
// string, body and path, error messages, and outbound dependency URLs.
//
// Everything here fails closed: when Pulse cannot confidently strip secrets
// from a value, it stores a size marker instead of the value.

const redactedPlaceholder = "[REDACTED]"

// truncatedMarker is appended to a captured JSON body that ended before the
// document did — either cut off at Errors.MaxBodySize or malformed. Nothing
// after the cut-off point is emitted.
const truncatedMarker = " …[truncated]"

// defaultSensitiveHeaders are redacted in captured request context.
var defaultSensitiveHeaders = []string{
	"authorization", "cookie", "set-cookie", "proxy-authorization",
	"x-api-key", "x-auth-token", "x-access-token", "x-refresh-token",
	"x-csrf-token", "x-xsrf-token", "x-amz-security-token", "x-goog-api-key",
}

// defaultSensitiveFieldNames are matched exactly against a normalized field
// name (see normalizeFieldName), so "cc_number", "ccNumber" and "cc-number"
// all match "ccnumber". Short or ambiguous names belong here rather than in
// defaultSensitiveFieldStems.
var defaultSensitiveFieldNames = []string{
	"pass", "pin", "auth", "authorization", "ssn", "cvc", "cvv", "ccnumber", "otp", "sessionid",
}

// defaultSensitiveFieldStems are matched as substrings of a normalized field
// name, so "newPassword", "stripe_token" and "X-Amz-Signature" are all
// caught. Only long, high-signal stems belong here — short ones like "pin"
// or "ssn" would match unrelated fields ("spinner", "classname").
var defaultSensitiveFieldStems = []string{
	"password", "passwd", "secret", "token", "apikey", "credential", "privatekey", "cardnumber", "signature",
}

// valueDetector finds one kind of secret inside free text.
type valueDetector struct {
	re *regexp.Regexp
	// replace returns the replacement for a match, or false to keep it.
	replace func(match string) (string, bool)
}

func fixedReplacement(s string) func(string) (string, bool) {
	return func(string) (string, bool) { return s, true }
}

// defaultValueDetectors catch secrets whatever field they appear in.
var defaultValueDetectors = []valueDetector{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
		fixedReplacement("[REDACTED:private-key]")},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}`),
		fixedReplacement("[REDACTED:jwt]")},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
		fixedReplacement("Bearer [REDACTED]")},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		fixedReplacement("[REDACTED:aws-key]")},
	// Card numbers: 13–19 digits, optionally grouped by spaces or dashes,
	// starting 3–6 (every major network) and passing the Luhn check. The
	// leading digit keeps millisecond timestamps (17…) out.
	{regexp.MustCompile(`\b[3-6](?:[ -]?\d){12,18}\b`), redactCardNumber},
}

func redactCardNumber(match string) (string, bool) {
	digits := make([]byte, 0, 19)
	for i := 0; i < len(match); i++ {
		if c := match[i]; c >= '0' && c <= '9' {
			digits = append(digits, c-'0')
		}
	}
	if len(digits) < 13 || len(digits) > 19 || !luhnValid(digits) {
		return match, false
	}
	return "[REDACTED:card]", true
}

func luhnValid(digits []byte) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		v := int(digits[i])
		if double {
			if v *= 2; v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return sum%10 == 0
}

// redactor applies one compiled set of redaction rules.
type redactor struct {
	headers map[string]bool // lowercase header names
	names   map[string]bool // normalized exact field names
	stems   []string        // normalized field-name substrings
	values  []valueDetector
	hook    func(*RequestContext)
}

// defaultRedactor applies only the built-in rules.
var defaultRedactor = newRedactor(RedactionConfig{})

func newRedactor(cfg RedactionConfig) *redactor {
	r := &redactor{headers: make(map[string]bool), names: make(map[string]bool), hook: cfg.Hook}
	if !cfg.DisableDefaults {
		for _, h := range defaultSensitiveHeaders {
			r.headers[h] = true
		}
		for _, n := range defaultSensitiveFieldNames {
			r.names[n] = true
		}
		r.stems = append(r.stems, defaultSensitiveFieldStems...)
		r.values = append(r.values, defaultValueDetectors...)
	}
	for _, h := range cfg.Headers {
		r.headers[strings.ToLower(h)] = true
	}
	for _, n := range cfg.Fields {
		if n := normalizeFieldName(n); n != "" {
			r.names[n] = true
		}
	}
	for _, s := range cfg.FieldContains {
		if s := normalizeFieldName(s); s != "" {
			r.stems = append(r.stems, s)
		}
	}
	for _, re := range cfg.ValuePatterns {
		if re != nil {
			r.values = append(r.values, valueDetector{re, fixedReplacement(redactedPlaceholder)})
		}
	}
	return r
}

// sensitiveField reports whether values of the named body field, form key
// or query parameter should be redacted.
func (r *redactor) sensitiveField(name string) bool {
	n := normalizeFieldName(name)
	if r.names[n] {
		return true
	}
	for _, stem := range r.stems {
		if strings.Contains(n, stem) {
			return true
		}
	}
	return false
}

func (r *redactor) sensitiveHeader(name string) bool {
	return r.headers[strings.ToLower(name)]
}

// scrubValue replaces every secret the value detectors find in s.
func (r *redactor) scrubValue(s string) string {
	if s == "" {
		return s
	}
	for _, d := range r.values {
		s = d.re.ReplaceAllStringFunc(s, func(m string) string {
			if rep, ok := d.replace(m); ok {
				return rep
			}
			return m
		})
	}
	return s
}

// headerMap flattens h to its first values, redacting sensitive headers and
// scrubbing the rest.
func (r *redactor) headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for key, values := range h {
		switch {
		case r.sensitiveHeader(key):
			out[key] = redactedPlaceholder
		case len(values) > 0:
			out[key] = r.scrubValue(values[0])
		}
	}
	return out
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
// JSON without one; body sniffs such bodies.
func redactableContentType(contentType string) bool {
	switch ct := mediaType(contentType); {
	case ct == "", ct == "application/json", strings.HasSuffix(ct, "+json"),
		ct == "application/x-www-form-urlencoded":
		return true
	}
	return false
}

// body returns a copy of a request body that is safe to store:
//
//   - JSON (application/json, */*+json, or no Content-Type but JSON-shaped):
//     the value of every sensitive key is replaced, at any depth, and other
//     string values are scrubbed. Works on truncated and malformed
//     documents — see jsonBody.
//   - application/x-www-form-urlencoded: as for a query string.
//   - anything else: replaced wholesale by a size marker.
func (r *redactor) body(contentType string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	switch ct := mediaType(contentType); {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		return r.jsonBody(body)
	case ct == "application/x-www-form-urlencoded":
		return []byte(r.query(string(body)))
	case ct == "" && looksLikeJSON(body):
		return r.jsonBody(body)
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

// jsonBody re-emits body token by token, replacing the value of every
// sensitive key — scalar or whole object/array — with the placeholder, and
// scrubbing every other string value.
//
// Streaming (rather than decode-then-marshal) keeps the original key order
// and, crucially, still works on a body cut off at MaxBodySize or otherwise
// malformed: everything before the problem is emitted redacted, then
// truncatedMarker, and nothing after it.
func (r *redactor) jsonBody(body []byte) []byte {
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
				redactNext = r.sensitiveField(key)
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
			writeJSONString(&out, r.scrubValue(t))
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

// readableEscape percent-encodes s for a query string but leaves the
// characters of redaction markers readable.
var readableEscape = strings.NewReplacer("%5B", "[", "%5D", "]", "%3A", ":")

// query redacts the values of sensitive keys in a URL query string or
// form-encoded body, and scrubs the other values. Pair order and the
// original encoding of everything it leaves alone are preserved. Pairs are
// handled independently, so a malformed or truncated pair never causes the
// rest to be kept raw.
func (r *redactor) query(raw string) string {
	if raw == "" {
		return raw
	}
	pairs := strings.Split(raw, "&")
	changed := false
	for i, pair := range pairs {
		key, value, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		if r.sensitiveField(name) {
			pairs[i] = key + "=" + redactedPlaceholder
			changed = true
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			decoded = value
		}
		if scrubbed := r.scrubValue(decoded); scrubbed != decoded {
			pairs[i] = key + "=" + readableEscape.Replace(url.QueryEscape(scrubbed))
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return strings.Join(pairs, "&")
}

// url renders u for storage with sensitive query parameters redacted, other
// values and the path scrubbed, and any userinfo password masked.
func (r *redactor) url(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	cp.RawQuery = r.query(cp.RawQuery)
	if scrubbed := r.scrubValue(cp.Path); scrubbed != cp.Path {
		cp.Path, cp.RawPath = scrubbed, ""
	}
	return cp.Redacted()
}

// errorText renders err for storage. An HTTP client error (*url.Error)
// embeds the full request URL, query-string credentials included, so it is
// rebuilt around the redacted URL; the result is then scrubbed.
func (r *redactor) errorText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		cp := *ue
		if u, perr := url.Parse(ue.URL); perr == nil {
			cp.URL = r.url(u)
		} else {
			cp.URL = "[unparseable URL]"
		}
		// Replace the url.Error in the message: its text is a prefix of the
		// wrapped error's text only when err is the url.Error itself.
		if err == error(ue) {
			return r.scrubValue(cp.Error())
		}
		return r.scrubValue(strings.Replace(err.Error(), ue.Error(), cp.Error(), 1))
	}
	return r.scrubValue(err.Error())
}

// finishRecord applies the final redaction to an error record before it is
// stored or broadcast: the error message is scrubbed — and the fingerprint
// recomputed from the scrubbed message, so embedded values don't split one
// error into many groups — then the user hook runs on the request context.
func (r *redactor) finishRecord(rec *ErrorRecord) {
	if msg := r.scrubValue(rec.ErrorMessage); msg != rec.ErrorMessage {
		rec.ErrorMessage = msg
		rec.Fingerprint = generateFingerprint(rec.Method, rec.Route, msg)
	}
	if r.hook != nil && rec.RequestContext != nil {
		r.runHook(rec)
	}
}

// runHook calls the user hook, reducing the context to method and path if
// it panics — failing closed rather than storing what it was meant to clean.
func (r *redactor) runHook(rec *ErrorRecord) {
	defer func() {
		if recover() != nil {
			rec.RequestContext = &RequestContext{Method: rec.RequestContext.Method, Path: rec.RequestContext.Path}
		}
	}()
	r.hook(rec.RequestContext)
}

// rescrub re-applies redaction to a stored request context — used to clean
// records written by earlier versions.
func (r *redactor) rescrub(rc *RequestContext) {
	for k, v := range rc.Headers {
		if r.sensitiveHeader(k) {
			rc.Headers[k] = redactedPlaceholder
		} else {
			rc.Headers[k] = r.scrubValue(v)
		}
	}
	rc.Path = r.scrubValue(rc.Path)
	rc.Query = r.query(rc.Query)
	if rc.Body != "" && !strings.HasPrefix(rc.Body, "[body omitted") {
		rc.Body = string(r.body(rc.ContentType, []byte(rc.Body)))
	}
}
