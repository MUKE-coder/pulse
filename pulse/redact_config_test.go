package pulse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRedactor_ValueDetectors(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, in, want string }{
		{"card with spaces", "charge 4111 1111 1111 1111 declined", "charge [REDACTED:card] declined"},
		{"card with dashes", "5500-0000-0000-0004", "[REDACTED:card]"},
		{"amex", "card 378282246310005", "card [REDACTED:card]"},
		{"fails Luhn: kept", "order 4111111111111112", "order 4111111111111112"},
		{"millisecond timestamp: kept", "at 1768478400000", "at 1768478400000"},
		{"long numeric id: kept", "id 41111111111111111111111", "id 41111111111111111111111"},
		{"jwt", "token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dGhpc2lzYXNpZw rejected", "token [REDACTED:jwt] rejected"},
		{"bearer", "sent Bearer abc123def456ghi", "sent Bearer [REDACTED]"},
		{"aws key", "key AKIAIOSFODNN7EXAMPLE leaked", "key [REDACTED:aws-key] leaked"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----", "[REDACTED:private-key]"},
		{"plain text: kept", "user bob not found", "user bob not found"},
	}
	for _, tc := range cases {
		if got := defaultRedactor.scrubValue(tc.in); got != tc.want {
			t.Errorf("%s: scrubValue(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestRedactor_ConfigExtendsDefaults(t *testing.T) {
	t.Parallel()
	r := newRedactor(RedactionConfig{
		Fields:        []string{"date_of_birth"},
		FieldContains: []string{"medical"},
		Headers:       []string{"X-Tenant-Secret"},
		ValuePatterns: []*regexp.Regexp{regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
	})
	for _, name := range []string{"dateOfBirth", "medicalRecordNo", "password"} {
		if !r.sensitiveField(name) {
			t.Errorf("sensitiveField(%q) = false, want true", name)
		}
	}
	if !r.sensitiveHeader("x-tenant-secret") || !r.sensitiveHeader("Authorization") {
		t.Error("expected both the custom and the built-in header to be sensitive")
	}
	if got := r.scrubValue("ssn 123-45-6789 on file"); got != "ssn [REDACTED] on file" {
		t.Errorf("custom pattern: got %q", got)
	}

	bare := newRedactor(RedactionConfig{DisableDefaults: true, Fields: []string{"pin_code"}})
	if bare.sensitiveField("password") || !bare.sensitiveField("pinCode") {
		t.Error("DisableDefaults should leave only the configured fields")
	}
	if got := bare.scrubValue("4111 1111 1111 1111"); got != "4111 1111 1111 1111" {
		t.Errorf("DisableDefaults should disable value detectors, got %q", got)
	}
}

func TestRedactor_ScrubsNonSensitiveValues(t *testing.T) {
	t.Parallel()
	got := string(defaultRedactor.jsonBody([]byte(`{"note":"card 4111 1111 1111 1111","n":4111111111111111}`)))
	want := `{"note":"card [REDACTED:card]","n":4111111111111111}` // numbers aren't text; only strings are scrubbed
	if got != want {
		t.Errorf("jsonBody = %s, want %s", got, want)
	}
	if got := defaultRedactor.query("q=pay%204111%201111%201111%201111&page=2"); got != "q=pay+[REDACTED:card]&page=2" {
		t.Errorf("query = %q", got)
	}
}

func TestRedactor_FinishRecord(t *testing.T) {
	t.Parallel()
	a := buildErrorRecord("POST", "/pay", "charge failed for 4111 1111 1111 1111", ErrorTypeInternal, "", nil, "")
	b := buildErrorRecord("POST", "/pay", "charge failed for 5500 0000 0000 0004", ErrorTypeInternal, "", nil, "")
	defaultRedactor.finishRecord(&a)
	defaultRedactor.finishRecord(&b)
	if a.ErrorMessage != "charge failed for [REDACTED:card]" {
		t.Errorf("message = %q", a.ErrorMessage)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Error("errors differing only in a redacted value should group together")
	}

	hooked := newRedactor(RedactionConfig{Hook: func(rc *RequestContext) { rc.Headers["X-Tenant"] = "hidden" }})
	rec := ErrorRecord{RequestContext: &RequestContext{Method: "GET", Path: "/x", Headers: map[string]string{"X-Tenant": "acme"}}}
	hooked.finishRecord(&rec)
	if rec.RequestContext.Headers["X-Tenant"] != "hidden" {
		t.Errorf("hook did not run: %+v", rec.RequestContext)
	}

	panicky := newRedactor(RedactionConfig{Hook: func(*RequestContext) { panic("boom") }})
	rec = ErrorRecord{RequestContext: &RequestContext{Method: "GET", Path: "/x", Body: `{"secret_sauce":"x"}`}}
	panicky.finishRecord(&rec)
	if rc := rec.RequestContext; rc.Body != "" || rc.Method != "GET" || rc.Path != "/x" {
		t.Errorf("a panicking hook should leave only method and path, got %+v", rc)
	}
}

// An outbound request failure's message embeds the full URL; the stored
// error must not carry its credentials.
func TestRedactor_ErrorTextRedactsURLErrors(t *testing.T) {
	t.Parallel()
	_, err := http.Get("http://svc:hunter2@127.0.0.1:1/charge?api_key=sk_live_123&amount=5")
	if err == nil {
		t.Skip("expected a connection error")
	}
	got := defaultRedactor.errorText(err)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "sk_live_123") {
		t.Fatalf("error text leaks credentials: %s", got)
	}
	if !strings.Contains(got, "amount=5") || !strings.Contains(got, "/charge") {
		t.Errorf("error text lost the non-sensitive parts: %s", got)
	}

	wrapped := fmt.Errorf("calling stripe: %w", err)
	if got := defaultRedactor.errorText(wrapped); strings.Contains(got, "sk_live_123") || !strings.HasPrefix(got, "calling stripe: ") {
		t.Errorf("wrapped error text = %s", got)
	}
}

// The review's "body-redaction verification": secrets sent in a failing
// request must not appear anywhere Pulse keeps or serves data — the API,
// both export formats, and the raw SQLite file.
func TestRedaction_EndToEndSecretsNeverStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "redact.db")
	router := gin.New()
	p := Mount(context.Background(), router, nil, WithDevMode(), WithSQLite(path), WithRedactFields("dob"))
	router.POST("/pay", func(c *gin.Context) {
		_ = c.Error(fmt.Errorf("charge failed for card 4111 1111 1111 1111"))
		c.Status(http.StatusBadGateway)
	})

	secrets := []string{"hunter2", "4111", "1990-01-01", "eyJhbGci", "sk_live_x", "s3ss10n"}
	body := `{"password":"hunter2","card":{"number":"4111-1111-1111-1111"},"dob":"1990-01-01",` +
		`"note":"token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl"}`
	req := httptest.NewRequest("POST", "/pay?api_key=sk_live_x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer abcdefghijklmnop")
	req.Header.Set("Cookie", "session=s3ss10n")
	router.ServeHTTP(httptest.NewRecorder(), req)

	token := signJWT(jwtClaims{Username: "t", Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix()},
		p.config.Dashboard.SecretKey)
	api := func(method, path, reqBody string) string {
		r := httptest.NewRequest(method, path, strings.NewReader(reqBody))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	var list []ErrorRecord
	if err := json.Unmarshal([]byte(api("GET", "/pulse/api/errors?range=1h", "")), &list); err != nil || len(list) != 1 {
		t.Fatalf("errors list: %v (%d records)", err, len(list))
	}
	served := map[string]string{
		"error detail": api("GET", "/pulse/api/errors/"+list[0].ID, ""),
		"json export":  api("POST", "/pulse/api/data/export", `{"format":"json","type":"errors","range":"1h"}`),
		"csv export":   api("POST", "/pulse/api/data/export", `{"format":"csv","type":"errors","range":"1h"}`),
	}
	if !strings.Contains(served["error detail"], "[REDACTED:card]") {
		t.Errorf("error detail should show redaction markers: %s", served["error detail"])
	}

	if err := p.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for _, suffix := range []string{"", "-wal"} {
		if raw, err := os.ReadFile(path + suffix); err == nil {
			served["sqlite file"+suffix] = string(raw)
		}
	}

	for where, content := range served {
		for _, s := range secrets {
			if strings.Contains(content, s) {
				t.Errorf("%s contains secret %q", where, s)
			}
		}
	}
}

// Records written by v1.0.0 are cleaned once, on first open by this version.
func TestSQLite_ScrubMigrationCleansOldRecords(t *testing.T) {
	s := newSQLiteForTest(t)
	now := time.Now().UnixNano()
	legacy := func(fp, msg string, rc RequestContext) {
		b, _ := json.Marshal(rc)
		if _, err := s.db.Exec(`INSERT INTO errors (fingerprint, id, method, route, error_message, error_type, request_ctx, first_seen, last_seen)
			VALUES (?, ?, 'POST', '/x', ?, 'internal', ?, ?, ?)`, fp, fp, msg, string(b), now, now); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// v1.0.0 stored truncated JSON and non-JSON bodies raw, and query strings unredacted.
	legacy("a", "card 4111 1111 1111 1111 declined", RequestContext{
		ContentType: "application/json", Body: `{"user":"bob","password":"hunt`, Query: "token=abc"})
	legacy("b", "boom", RequestContext{ContentType: "text/plain", Body: "password hunter2"})

	n, err := s.scrubStoredErrors(defaultRedactor)
	if err != nil || n != 2 {
		t.Fatalf("first scrub: %d changed, err %v; want 2", n, err)
	}
	errs, _ := s.GetErrors(ErrorFilter{})
	for _, e := range errs {
		all := e.ErrorMessage + e.RequestContext.Body + e.RequestContext.Query
		for _, secret := range []string{"hunt", "4111", "abc"} {
			if strings.Contains(all, secret) {
				t.Errorf("record %s still contains %q: %+v", e.Fingerprint, secret, e.RequestContext)
			}
		}
	}
	if n, err := s.scrubStoredErrors(defaultRedactor); err != nil || n != 0 {
		t.Fatalf("second scrub: %d changed, err %v; want a no-op", n, err)
	}
}

// FuzzRedactJSONBody plants a secret under a sensitive key next to arbitrary
// content, cuts the document anywhere, and checks the secret never survives.
func FuzzRedactJSONBody(f *testing.F) {
	f.Add("hello", 10)
	f.Add(`quote " and \ backslash`, 25)
	f.Add("{nested}", 1000)
	f.Fuzz(func(t *testing.T, other string, cut int) {
		otherJSON, _ := json.Marshal(other)
		doc := `{"other":` + string(otherJSON) + `,"password":"PLANTED-SECRET-VALUE"}`
		if cut < 0 {
			cut = -cut
		}
		cut %= len(doc) + 1
		if out := string(defaultRedactor.jsonBody([]byte(doc[:cut]))); strings.Contains(out, "PLANTED") {
			t.Fatalf("secret survived: %s", out)
		}
	})
}
