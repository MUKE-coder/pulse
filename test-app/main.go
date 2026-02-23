package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func main() {
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}

	router := gin.New()
	pulse.Mount(router, db, pulse.Config{
		AppName: "Test App",
		DevMode: true,
	})

	router.GET("/api/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"pong": true})
	})

	tests := []struct {
		path       string
		wantCode   int
		wantHeader string // expected Location header for redirects
		wantBody   string // substring to check in body
	}{
		// Redirects
		{"/pulse", 301, "/pulse/ui/", ""},
		{"/pulse/", 301, "/pulse/ui/", ""},
		{"/pulse/ui", 301, "/pulse/ui/", ""},

		// Dashboard SPA (should serve index.html)
		{"/pulse/ui/", 200, "", "<div id=\"root\">"},
		{"/pulse/ui/login", 200, "", "<div id=\"root\">"},
		{"/pulse/ui/routes", 200, "", "<div id=\"root\">"},
		{"/pulse/ui/errors", 200, "", "<div id=\"root\">"},

		// Static assets
		{"/pulse/ui/assets/index-CKc0di6z.js", 200, "", ""},
		{"/pulse/ui/assets/index-DmEaoMqh.css", 200, "", ""},

		// API (auth required)
		{"/pulse/api/auth/login", -1, "", ""}, // POST only

		// Health (public)
		{"/pulse/health", 200, "", "status"},
		{"/pulse/health/live", 200, "", "status"},

		// App routes still work
		{"/api/ping", 200, "", "pong"},
	}

	pass := 0
	fail := 0

	for _, tt := range tests {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", tt.path, nil)
		router.ServeHTTP(w, req)

		if tt.wantCode == -1 {
			// Skip code check (e.g. POST-only route)
			fmt.Printf("  SKIP %s → %d\n", tt.path, w.Code)
			continue
		}

		ok := true
		if w.Code != tt.wantCode {
			fmt.Printf("  FAIL %s → got %d, want %d\n", tt.path, w.Code, tt.wantCode)
			ok = false
		}
		if tt.wantHeader != "" {
			loc := w.Header().Get("Location")
			if loc != tt.wantHeader {
				fmt.Printf("  FAIL %s → Location=%q, want %q\n", tt.path, loc, tt.wantHeader)
				ok = false
			}
		}
		if tt.wantBody != "" && !strings.Contains(w.Body.String(), tt.wantBody) {
			fmt.Printf("  FAIL %s → body missing %q (got %d bytes)\n", tt.path, tt.wantBody, w.Body.Len())
			ok = false
		}
		if ok {
			fmt.Printf("  PASS %s → %d\n", tt.path, w.Code)
			pass++
		} else {
			fail++
		}
	}

	fmt.Printf("\n%d passed, %d failed\n", pass, fail)
	if fail > 0 {
		log.Fatal("TESTS FAILED")
	}

	// Also test login flow
	fmt.Println("\nTesting login flow:")
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/pulse/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"pulse"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	fmt.Printf("  Login: %d %s\n", w.Code, w.Body.String()[:min(80, w.Body.Len())])
	if w.Code != http.StatusOK {
		log.Fatal("Login failed!")
	}

	fmt.Println("\nAll tests passed! Dashboard is working.")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
