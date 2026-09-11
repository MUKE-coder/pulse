package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Request-body handling in the error middleware: handlers must always
// receive the complete body, and what Pulse stores must never contain
// secrets — including when the body is cut off at MaxBodySize.

func newBodyTestRouter(t *testing.T, maxBodySize int) (*gin.Engine, *Pulse, *[]byte) {
	t.Helper()
	p := newPulse(context.Background(), applyDefaults(Config{
		Errors: ErrorConfig{MaxBodySize: maxBodySize},
	}))
	p.storage = NewMemoryStorage("test")
	t.Cleanup(func() { p.Shutdown() })

	received := new([]byte)
	router := gin.New()
	router.Use(newErrorMiddleware(p))
	router.POST("/submit", func(c *gin.Context) {
		*received, _ = io.ReadAll(c.Request.Body)
		c.Error(errors.New("validation failed"))
		c.Status(http.StatusInternalServerError)
	})
	return router, p, received
}

// waitForErrorRecord polls for the error record the middleware stores
// asynchronously.
func waitForErrorRecord(t *testing.T, p *Pulse) ErrorRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errs, _ := p.storage.GetErrors(ErrorFilter{}); len(errs) > 0 {
			return errs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no error record stored")
	return ErrorRecord{}
}

func postBody(router *gin.Engine, target, contentType, body string) {
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	router.ServeHTTP(httptest.NewRecorder(), req)
}

// TestErrorMiddleware_HandlerReceivesFullBody is the regression test for
// v1.0.0 handing handlers only the first MaxBodySize bytes of every body.
func TestErrorMiddleware_HandlerReceivesFullBody(t *testing.T) {
	router, p, received := newBodyTestRouter(t, 4096)

	payload := `{"items":[` + strings.Repeat(`{"name":"widget","qty":1},`, 2500) + `{"name":"last","qty":2}]}`
	postBody(router, "/submit", "application/json", payload)

	if string(*received) != payload {
		t.Fatalf("handler received %d bytes, want all %d", len(*received), len(payload))
	}
	if !json.Valid(*received) {
		t.Fatal("handler received invalid JSON")
	}

	rc := waitForErrorRecord(t, p).RequestContext
	if !rc.BodyTruncated {
		t.Error("expected BodyTruncated for a body larger than MaxBodySize")
	}
	if len(rc.Body) > 4096+len(truncatedMarker) {
		t.Errorf("captured body is %d bytes, want at most MaxBodySize", len(rc.Body))
	}
}

// TestErrorMiddleware_TruncatedJSONStaysRedacted cuts the body off inside a
// password value. v1.0.0 failed to parse the truncated JSON and stored it raw.
func TestErrorMiddleware_TruncatedJSONStaysRedacted(t *testing.T) {
	router, p, received := newBodyTestRouter(t, 40)

	payload := `{"username":"bob","password":"S3CR3T-VALUE-THAT-CROSSES-THE-LIMIT"}`
	postBody(router, "/submit", "application/json", payload)

	if string(*received) != payload {
		t.Fatalf("handler received %q, want the full body", *received)
	}
	rc := waitForErrorRecord(t, p).RequestContext
	if strings.Contains(rc.Body, "S3C") {
		t.Fatalf("stored body leaks the password: %q", rc.Body)
	}
	if !strings.Contains(rc.Body, `"username":"bob"`) || !rc.BodyTruncated {
		t.Errorf("expected the redacted prefix, marked truncated; got %q (truncated=%v)", rc.Body, rc.BodyTruncated)
	}
}

func TestErrorMiddleware_UnredactableBodyIsOmitted(t *testing.T) {
	router, p, received := newBodyTestRouter(t, 4096)

	payload := "card 4111111111111111, password hunter2"
	postBody(router, "/submit", "text/plain", payload)

	if string(*received) != payload {
		t.Fatalf("handler received %q, want the full body", *received)
	}
	rc := waitForErrorRecord(t, p).RequestContext
	want := "[body omitted: 39 bytes of text/plain]"
	if rc.Body != want {
		t.Errorf("stored body = %q, want %q", rc.Body, want)
	}
}

func TestErrorMiddleware_RedactsFormAndQuery(t *testing.T) {
	router, p, _ := newBodyTestRouter(t, 4096)

	postBody(router, "/submit?token=abc123&page=2", "application/x-www-form-urlencoded",
		"username=bob&password=hunter2&newPassword=hunter3")

	rc := waitForErrorRecord(t, p).RequestContext
	if rc.Query != "token=[REDACTED]&page=2" {
		t.Errorf("Query = %q, want token redacted", rc.Query)
	}
	if strings.Contains(rc.Body, "hunter") || !strings.Contains(rc.Body, "username=bob") {
		t.Errorf("form body not redacted correctly: %q", rc.Body)
	}
}
