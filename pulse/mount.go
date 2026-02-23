package pulse

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/MUKE-coder/pulse/ui"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Mount registers Pulse middleware, GORM plugin, and dashboard routes on the given
// Gin router and GORM database. It returns a *Pulse instance for further configuration
// (e.g., adding health checks, wrapping HTTP clients).
//
// Usage:
//
//	p := pulse.Mount(router, db, pulse.Config{})
//	// Dashboard available at http://localhost:8080/pulse
func Mount(router *gin.Engine, db *gorm.DB, configs ...Config) *Pulse {
	// Merge user config with defaults
	var cfg Config
	if len(configs) > 0 {
		cfg = configs[0]
	}
	cfg = applyDefaults(cfg)

	// Create the Pulse engine
	p := newPulse(cfg)

	// Initialize storage
	p.storage = NewMemoryStorage(cfg.AppName)

	// Register GORM query tracking plugin
	if db != nil && boolValue(cfg.Database.Enabled) {
		plugin := &PulsePlugin{
			pulse:     p,
			n1Tracker: make(map[string]map[string]int),
		}
		p.gormPlugin = plugin
		if err := db.Use(plugin); err != nil {
			log.Printf("[pulse] warning: failed to register GORM plugin: %v", err)
		}
	}

	// Start runtime metrics sampler
	if boolValue(cfg.Runtime.Enabled) {
		p.runtimeSampler = newRuntimeSampler(p)
	}

	// Start aggregation engine (computes rollups, trends, overview cache)
	p.aggregator = newAggregator(p)

	// Auto-register built-in database health check
	if db != nil && boolValue(cfg.Health.Enabled) {
		p.AddHealthCheck(DatabaseHealthCheck(db))
	}

	// Start health check runner
	if boolValue(cfg.Health.Enabled) {
		p.healthRunner = newHealthRunner(p)
	}

	// Start alert engine
	if boolValue(cfg.Alerts.Enabled) {
		p.alertEngine = newAlertEngine(p)
	}

	// Register error tracking middleware (outermost — catches panics from all handlers)
	if boolValue(cfg.Errors.Enabled) {
		router.Use(newErrorMiddleware(p))
	}

	// Register request tracing middleware (before routes so it captures all requests)
	if boolValue(cfg.Tracing.Enabled) {
		router.Use(newTracingMiddleware(p))
	}

	// Start WebSocket hub
	p.wsHub = newWebSocketHub(p)
	p.startBackground("websocket-hub", func(ctx context.Context) {
		p.wsHub.run()
	})

	// Register public health endpoints (no auth required)
	if boolValue(cfg.Health.Enabled) {
		registerHealthRoutes(router, p)
	}

	// Register WebSocket endpoint
	registerWebSocketRoute(router, p)

	// Register Prometheus endpoint (public, no auth)
	if cfg.Prometheus.Enabled {
		registerPrometheusRoute(router, p)
	}

	// Register REST API endpoints
	registerAPIRoutes(router, p)

	// Serve embedded React dashboard
	prefix := cfg.Prefix
	registerDashboardRoutes(router, prefix, cfg)

	log.Printf("[pulse] mounted at %s — dashboard: http://localhost:8080%s/ui/", prefix, prefix)
	if cfg.DevMode {
		log.Printf("[pulse] dev mode enabled — verbose logging active")
	}

	return p
}

// registerDashboardRoutes serves the embedded React dashboard or falls back to a placeholder.
func registerDashboardRoutes(router *gin.Engine, prefix string, cfg Config) {
	distFS, err := ui.DistFS()
	if err != nil {
		log.Printf("[pulse] failed to load embedded UI: %v (serving fallback)", err)
		router.GET(prefix+"/ui/*filepath", func(c *gin.Context) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(placeholderHTML(cfg)))
		})
		return
	}

	// Read index.html once at startup
	indexHTML, readErr := fs.ReadFile(distFS, "index.html")
	if readErr != nil {
		log.Printf("[pulse] warning: index.html not found in embedded UI: %v", readErr)
		indexHTML = []byte(placeholderHTML(cfg))
	}

	// Redirect /pulse → /pulse/ui/
	router.GET(prefix, func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, prefix+"/ui/")
	})
	router.GET(prefix+"/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, prefix+"/ui/")
	})

	// Serve dashboard SPA
	router.GET(prefix+"/ui/*filepath", func(c *gin.Context) {
		filePath := strings.TrimPrefix(c.Param("filepath"), "/")
		if filePath == "" || filePath == "/" {
			c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
			return
		}

		// Try to serve the static file (JS, CSS, images)
		data, readErr := fs.ReadFile(distFS, filePath)
		if readErr != nil {
			// SPA fallback: serve index.html for client-side routing
			c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
			return
		}

		contentType := "application/octet-stream"
		switch {
		case strings.HasSuffix(filePath, ".html"):
			contentType = "text/html; charset=utf-8"
		case strings.HasSuffix(filePath, ".js"):
			contentType = "application/javascript"
		case strings.HasSuffix(filePath, ".css"):
			contentType = "text/css"
		case strings.HasSuffix(filePath, ".json"):
			contentType = "application/json"
		case strings.HasSuffix(filePath, ".svg"):
			contentType = "image/svg+xml"
		case strings.HasSuffix(filePath, ".png"):
			contentType = "image/png"
		case strings.HasSuffix(filePath, ".ico"):
			contentType = "image/x-icon"
		case strings.HasSuffix(filePath, ".woff2"):
			contentType = "font/woff2"
		case strings.HasSuffix(filePath, ".woff"):
			contentType = "font/woff"
		}
		c.Data(http.StatusOK, contentType, data)
	})
}

// placeholderHTML returns a simple HTML page confirming Pulse is mounted.
func placeholderHTML(cfg Config) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s — Pulse</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: #0a0a0f;
            color: #e2e8f0;
            display: flex;
            align-items: center;
            justify-content: center;
            min-height: 100vh;
        }
        .container {
            text-align: center;
            max-width: 600px;
            padding: 2rem;
        }
        .pulse-icon {
            width: 80px;
            height: 80px;
            margin: 0 auto 1.5rem;
            border-radius: 20px;
            background: linear-gradient(135deg, #6366f1, #8b5cf6);
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 2.5rem;
            animation: pulse 2s ease-in-out infinite;
        }
        @keyframes pulse {
            0%%, 100%% { transform: scale(1); opacity: 1; }
            50%% { transform: scale(1.05); opacity: 0.8; }
        }
        h1 {
            font-size: 2rem;
            font-weight: 700;
            margin-bottom: 0.5rem;
            background: linear-gradient(135deg, #6366f1, #a78bfa);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
        }
        .subtitle {
            color: #94a3b8;
            font-size: 1.1rem;
            margin-bottom: 2rem;
        }
        .status {
            display: inline-flex;
            align-items: center;
            gap: 0.5rem;
            padding: 0.5rem 1rem;
            border-radius: 9999px;
            background: rgba(34, 197, 94, 0.1);
            border: 1px solid rgba(34, 197, 94, 0.3);
            color: #22c55e;
            font-size: 0.9rem;
            font-weight: 500;
        }
        .status-dot {
            width: 8px;
            height: 8px;
            border-radius: 50%%;
            background: #22c55e;
            animation: blink 1.5s ease-in-out infinite;
        }
        @keyframes blink {
            0%%, 100%% { opacity: 1; }
            50%% { opacity: 0.3; }
        }
        .info {
            margin-top: 2rem;
            color: #64748b;
            font-size: 0.85rem;
        }
        .info code {
            background: rgba(99, 102, 241, 0.1);
            color: #a78bfa;
            padding: 0.2rem 0.5rem;
            border-radius: 4px;
            font-family: 'SF Mono', 'Fira Code', monospace;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="pulse-icon">💜</div>
        <h1>Pulse</h1>
        <p class="subtitle">Observability & Performance Monitoring for %s</p>
        <div class="status">
            <span class="status-dot"></span>
            Engine Mounted — Dashboard Coming Soon
        </div>
        <p class="info">
            The full React dashboard will replace this page.<br>
            Prefix: <code>%s</code> | Storage: <code>%s</code>
        </p>
    </div>
</body>
</html>`, cfg.AppName, cfg.AppName, cfg.Prefix, storageDriverName(cfg.Storage.Driver))
}

func storageDriverName(d StorageDriver) string {
	switch d {
	case SQLite:
		return "SQLite"
	default:
		return "Memory"
	}
}

