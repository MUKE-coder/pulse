package pulse

import (
	"bytes"
	"fmt"
	"net/http"
	nethttppprof "net/http/pprof"
	"runtime"
	runtimepprof "runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// The Go runtime's own profiles, served as pprof protobuf so the standard
// tools read them with no extra plumbing:
//
//	go tool pprof "http://host/pulse/api/profile/heap?token=$PULSE_TOKEN"
//	go tool pprof -diff_base before.pprof after.pprof
//
// Pulse's flame graph samples goroutine stacks itself and answers "where are
// my goroutines sitting" with no dependencies at all. These answer the two
// questions a stack dump cannot: what allocates, and what contends — and
// give true CPU attribution, which sampling every goroutine's stack cannot.
//
// They sit behind the same double gate as the flame graph, because a heap
// profile holds strings from live objects.

const (
	defaultCPUProfileSeconds = 10
	maxCPUProfileSeconds     = 120
)

// cpuProfileMu keeps CPU profiles to one at a time: the runtime allows only
// one, and the second caller would otherwise get an error mid-download.
var cpuProfileMu sync.Mutex

// runtimeProfiles are the runtime's named profiles, with what each answers.
var runtimeProfiles = map[string]string{
	"heap":         "what is on the heap now",
	"allocs":       "everything allocated since the process started",
	"goroutine":    "every goroutine's stack",
	"mutex":        "where lock contention happens",
	"block":        "where goroutines wait",
	"threadcreate": "where OS threads are created",
}

// profilingAllowed reports whether profiling is permitted, answering the
// request itself when it isn't.
func profilingAllowed(c *gin.Context, p *Pulse) bool {
	if profilingPermitted(p.config) {
		return true
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": "profiling is disabled",
		"hint":  "set pulse.WithProfiling() AND export " + ProfileEnabledEnv + "=true",
	})
	return false
}

// profileIsOff reports whether a profile the runtime keeps off by default is
// still off, so an empty profile is never mistaken for "no contention".
func profileIsOff(cfg Config, name string) (reason, hint string, off bool) {
	switch name {
	case "mutex":
		if runtime.SetMutexProfileFraction(-1) == 0 { // -1 reads without changing
			return "mutex profiling is off: the runtime samples no contention until it is turned on",
				"mount with pulse.WithMutexProfiling(5) — it costs a little on every contended lock", true
		}
	case "block":
		if cfg.Profiling.BlockProfileRate <= 0 {
			return "block profiling is off: the runtime records no blocking events until it is turned on",
				"mount with pulse.WithBlockProfiling(10000) — it costs a little on every blocking operation", true
		}
	}
	return "", "", false
}

// runtimeProfileHandler serves one of the runtime's named profiles as pprof
// protobuf, or as text with ?debug=1.
func runtimeProfileHandler(p *Pulse, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !profilingAllowed(c, p) {
			return
		}
		profile := runtimepprof.Lookup(name)
		if profile == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown profile " + name})
			return
		}
		if reason, hint, off := profileIsOff(p.config, name); off {
			c.JSON(http.StatusConflict, gin.H{"error": reason, "hint": hint})
			return
		}

		debug := queryInt(c, "debug", 0)
		var out bytes.Buffer
		if err := profile.WriteTo(&out, debug); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if debug > 0 {
			c.Data(http.StatusOK, "text/plain; charset=utf-8", out.Bytes())
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "pulse-"+name+".pprof"))
		c.Data(http.StatusOK, "application/octet-stream", out.Bytes())
	}
}

// cpuProfileHandler samples the CPU for ?seconds= (default 10, capped at
// 120) and serves the profile.
func cpuProfileHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !profilingAllowed(c, p) {
			return
		}
		seconds := queryInt(c, "seconds", defaultCPUProfileSeconds)
		if seconds <= 0 {
			seconds = defaultCPUProfileSeconds
		}
		if seconds > maxCPUProfileSeconds {
			seconds = maxCPUProfileSeconds
		}
		if !cpuProfileMu.TryLock() {
			c.JSON(http.StatusConflict, gin.H{"error": "a CPU profile is already running; try again in a moment"})
			return
		}
		defer cpuProfileMu.Unlock()

		var out bytes.Buffer
		if err := runtimepprof.StartCPUProfile(&out); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		select {
		case <-time.After(time.Duration(seconds) * time.Second):
		case <-c.Request.Context().Done(): // the caller gave up; keep what we have
		}
		runtimepprof.StopCPUProfile()

		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "pulse-cpu.pprof"))
		c.Data(http.StatusOK, "application/octet-stream", out.Bytes())
	}
}

// bearerFromQuery lets tools that cannot set headers — go tool pprof — pass
// the dashboard token as ?token=, the way the WebSocket endpoint does.
func bearerFromQuery() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			if token := c.Query("token"); token != "" {
				c.Request.Header.Set("Authorization", "Bearer "+token)
			}
		}
		c.Next()
	}
}

// profileListHandler serves GET /pulse/api/profile/list: the profiles this
// process can produce, and which of them the runtime is actually keeping.
func profileListHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !profilingAllowed(c, p) {
			return
		}
		type entry struct {
			Name      string `json:"name"`
			Describes string `json:"describes"`
			Records   int    `json:"records"`
			Off       bool   `json:"off,omitempty"`
			Hint      string `json:"hint,omitempty"`
		}
		out := []entry{{Name: "cpu", Describes: "what is executing, sampled over ?seconds="}}
		for name, describes := range runtimeProfiles {
			e := entry{Name: name, Describes: describes}
			if profile := runtimepprof.Lookup(name); profile != nil {
				e.Records = profile.Count()
			}
			if _, hint, off := profileIsOff(p.config, name); off {
				e.Off, e.Hint = true, hint
			}
			out = append(out, e)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		c.JSON(http.StatusOK, gin.H{"profiles": out,
			"usage": "go tool pprof \"" + p.config.Prefix + "/api/profile/heap?token=<dashboard token>\""})
	}
}

// registerProfileRoutes mounts the runtime profiles, and — when
// Profiling.DebugHandlers is set — the standard net/http/pprof handlers
// under <prefix>/debug/pprof, which is where muscle memory reaches.
func registerProfileRoutes(router *gin.Engine, p *Pulse) {
	api := router.Group(p.config.Prefix+"/api", bearerFromQuery(), authMiddleware(p))
	for name := range runtimeProfiles {
		api.GET("/profile/"+name, runtimeProfileHandler(p, name))
	}
	api.GET("/profile/cpu", cpuProfileHandler(p))
	api.GET("/profile/list", profileListHandler(p))

	if !boolValue(p.config.Profiling.DebugHandlers) {
		return
	}
	debug := router.Group(p.config.Prefix+"/debug/pprof", bearerFromQuery(), authMiddleware(p))
	debug.GET("", debugPprofHandler(p))
	debug.GET("/", debugPprofHandler(p))
	debug.GET("/:name", debugPprofHandler(p))
	debug.POST("/:name", debugPprofHandler(p)) // symbol takes POST
}

// debugPprofHandler dispatches to the standard handlers. They read the
// profile name from a path they expect to be mounted at, so the request is
// rewritten to that path first.
func debugPprofHandler(p *Pulse) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !profilingAllowed(c, p) {
			return
		}
		name := strings.Trim(c.Param("name"), "/")
		r := c.Request.Clone(c.Request.Context())
		r.URL.Path = "/debug/pprof/" + name
		switch name {
		case "cmdline":
			nethttppprof.Cmdline(c.Writer, r)
		case "profile":
			nethttppprof.Profile(c.Writer, r)
		case "symbol":
			nethttppprof.Symbol(c.Writer, r)
		case "trace":
			nethttppprof.Trace(c.Writer, r)
		default:
			// Index serves the listing at the root and a named profile below it.
			nethttppprof.Index(c.Writer, r)
		}
	}
}
