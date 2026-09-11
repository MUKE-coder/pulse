package pulse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsSensitiveField(t *testing.T) {
	t.Parallel()
	sensitive := []string{
		"password", "Password", "newPassword", "new_password", "password_confirmation",
		"passwd", "pass", "pin", "PIN", "secret", "client_secret", "clientSecret",
		"token", "access_token", "refreshToken", "stripeToken", "csrf-token",
		"api_key", "apiKey", "X-Api-Key", "credential", "credentials",
		"private_key", "card_number", "cardNumber", "Card-Number", "cc_number",
		"cvv", "cvc", "ssn", "otp", "session_id", "X-Amz-Signature",
	}
	for _, name := range sensitive {
		if !defaultRedactor.sensitiveField(name) {
			t.Errorf("defaultRedactor.sensitiveField(%q) = false, want true", name)
		}
	}
	benign := []string{
		"description", "spinner", "author", "username", "email", "name",
		"classname", "key", "page", "id", "amount", "passenger",
	}
	for _, name := range benign {
		if defaultRedactor.sensitiveField(name) {
			t.Errorf("defaultRedactor.sensitiveField(%q) = true, want false", name)
		}
	}
}

func TestRedactJSONBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{
			name: "nested objects and arrays",
			in:   `{"user":{"name":"bob","password":"x"},"items":[{"token":"t1"},{"id":2}]}`,
			want: `{"user":{"name":"bob","password":"[REDACTED]"},"items":[{"token":"[REDACTED]"},{"id":2}]}`,
		},
		{
			name: "sensitive key with object value drops the whole subtree",
			in:   `{"auth":{"user":"bob","code":"123"},"after":1}`,
			want: `{"auth":"[REDACTED]","after":1}`,
		},
		{
			name: "sensitive key with array value drops the whole array",
			in:   `{"tokens":["a",["b"]],"n":1}`,
			want: `{"tokens":"[REDACTED]","n":1}`,
		},
		{
			name: "camelCase and kebab-case names",
			in:   `{"cardNumber":"4111111111111111","card-cvv":"1","cvv":"123"}`,
			want: `{"cardNumber":"[REDACTED]","card-cvv":"1","cvv":"[REDACTED]"}`,
		},
		{
			name: "key order and number literals preserved",
			in:   `{"z":1,"a":12345678901234567890,"f":1.50}`,
			want: `{"z":1,"a":12345678901234567890,"f":1.50}`,
		},
		{
			name: "no HTML escaping",
			in:   `{"html":"<b>&</b>"}`,
			want: `{"html":"<b>&</b>"}`,
		},
		{
			name: "top-level array",
			in:   `[{"password":"x"},"plain",[1,2],[]]`,
			want: `[{"password":"[REDACTED]"},"plain",[1,2],[]]`,
		},
		{
			name: "whitespace is compacted",
			in:   "{ \"a\" : 1 ,\n \"b\" : [ true , null ] }",
			want: `{"a":1,"b":[true,null]}`,
		},
		{
			name: "truncated inside a sensitive value",
			in:   `{"name":"bob","password":"hun`,
			want: `{"name":"bob","password":` + truncatedMarker,
		},
		{
			name: "truncated between members",
			in:   `{"name":"bob",`,
			want: `{"name":"bob"` + truncatedMarker,
		},
		{
			name: "empty object",
			in:   `{}`,
			want: `{}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(defaultRedactor.jsonBody([]byte(tc.in))); got != tc.want {
				t.Errorf("defaultRedactor.jsonBody(%s)\n got %s\nwant %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactJSONBody_MalformedIsCutOff(t *testing.T) {
	t.Parallel()
	got := string(defaultRedactor.jsonBody([]byte(`{"name" "bob","password":"hunter2"}`)))
	if !strings.HasSuffix(got, truncatedMarker) {
		t.Errorf("malformed JSON should end with the truncation marker, got %q", got)
	}
	if strings.Contains(got, "bob") || strings.Contains(got, "hunter2") {
		t.Errorf("nothing after the parse error may be emitted, got %q", got)
	}
}

// TestRedactJSONBody_TruncationNeverLeaks cuts a document containing secrets
// at every byte offset — as MaxBodySize truncation does — and checks no
// fragment of a secret ever survives redaction. v1.0.0 stored truncated
// bodies raw because they failed to parse.
func TestRedactJSONBody_TruncationNeverLeaks(t *testing.T) {
	t.Parallel()
	doc := `{"user":"bob","password":"S3CR3T-PASSWORD","card":{"card_number":"4111111111111111","exp":"12/30"},` +
		`"tags":["a","b"],"api_key":"AKIA-SECRET-KEY","auth":{"otp":"99887766"}}`
	secrets := []string{"S3C", "4111", "AKIA", "9988"}
	for cut := 0; cut <= len(doc); cut++ {
		out := string(defaultRedactor.jsonBody([]byte(doc[:cut])))
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Fatalf("cut at byte %d leaked %q: %s", cut, s, out)
			}
		}
	}
}

func TestRedactQuery_TruncationNeverLeaks(t *testing.T) {
	t.Parallel()
	form := "username=bob&password=S3CR3T-PASSWORD&card_number=4111111111111111&note=hi"
	for cut := 0; cut <= len(form); cut++ {
		out := defaultRedactor.query(form[:cut])
		if strings.Contains(out, "S3C") || strings.Contains(out, "4111") {
			t.Fatalf("cut at byte %d leaked a secret: %s", cut, out)
		}
	}
}

func TestRedactBody_ContentTypes(t *testing.T) {
	t.Parallel()
	jsonBody := []byte(`{"password":"hunter2","name":"bob"}`)
	cases := []struct {
		name, contentType string
		body              []byte
		want              string
	}{
		{"json with charset", "application/json; charset=utf-8", jsonBody, `{"password":"[REDACTED]","name":"bob"}`},
		{"vendor +json", "application/vnd.api+json", jsonBody, `{"password":"[REDACTED]","name":"bob"}`},
		{"no content type, JSON-shaped", "", jsonBody, `{"password":"[REDACTED]","name":"bob"}`},
		{"form", "application/x-www-form-urlencoded", []byte("name=bob&password=hunter2"), "name=bob&password=[REDACTED]"},
		{"no content type, not JSON", "", []byte("password=hunter2"), "[body omitted: 16 bytes of unknown content type]"},
		{"plain text", "text/plain", []byte("my password is hunter2"), "[body omitted: 22 bytes of text/plain]"},
		{"xml", "application/xml", []byte("<p>hunter2</p>"), "[body omitted: 14 bytes of application/xml]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(defaultRedactor.body(tc.contentType, tc.body)); got != tc.want {
				t.Errorf("defaultRedactor.body(%q) = %q, want %q", tc.contentType, got, tc.want)
			}
		})
	}
}

func TestRedactQuery(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"", ""},
		{"page=2&sort=asc", "page=2&sort=asc"},
		{"token=abc123&page=2", "token=[REDACTED]&page=2"},
		{"q=a%20b&api_key=zzz", "q=a%20b&api_key=[REDACTED]"},
		{"pass%77ord=x", "pass%77ord=[REDACTED]"},         // percent-encoded key
		{"%zz=1&password=x", "%zz=1&password=[REDACTED]"}, // malformed pair doesn't stop redaction
		{"debug&access_token=x", "debug&access_token=[REDACTED]"},
		{"password=a&password=b", "password=[REDACTED]&password=[REDACTED]"},
	}
	for _, tc := range cases {
		if got := defaultRedactor.query(tc.in); got != tc.want {
			t.Errorf("defaultRedactor.query(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWrapHTTPClient_RedactsURL checks that credentials in an outbound URL —
// a userinfo password and sensitive query parameters — never reach storage.
func TestWrapHTTPClient_RedactsURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := setupDepPulse(t)
	client := WrapHTTPClient(p, &http.Client{}, "creds-api")

	target := strings.Replace(server.URL, "http://", "http://svc:hunter2@", 1) +
		"/charge?api_key=sk_live_123&amount=5"
	resp, err := client.Get(target)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	var deps []DependencyMetric
	deadline := time.Now().Add(2 * time.Second)
	for len(deps) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		deps = p.storage.(*MemoryStorage).dependencies.GetAll()
	}
	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency metric, got %d", len(deps))
	}
	got := deps[0].URL
	for _, secret := range []string{"hunter2", "sk_live_123"} {
		if strings.Contains(got, secret) {
			t.Errorf("stored URL leaks %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "amount=5") || !strings.Contains(got, "/charge") {
		t.Errorf("stored URL lost non-sensitive parts: %s", got)
	}
}
