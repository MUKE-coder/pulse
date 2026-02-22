package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Product is a sample GORM model.
type Product struct {
	ID    uint    `gorm:"primaryKey" json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
}

// Order is a sample GORM model.
type Order struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ProductID uint      `json:"product_id"`
	Quantity  int       `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
}

func main() {
	// Initialize database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	db.AutoMigrate(&Product{}, &Order{})

	// Seed data
	products := []Product{
		{Name: "Widget", Price: 9.99},
		{Name: "Gadget", Price: 24.99},
		{Name: "Doohickey", Price: 14.99},
	}
	for _, p := range products {
		db.Create(&p)
	}

	router := gin.Default()

	// Mount Pulse with full configuration
	p := pulse.Mount(router, db, pulse.Config{
		Prefix:  "/pulse",
		AppName: "E-Commerce API",
		DevMode: true,

		Dashboard: pulse.DashboardConfig{
			Username: "admin",
			Password: "supersecret",
		},

		Tracing: pulse.TracingConfig{
			SlowRequestThreshold: 500 * time.Millisecond,
			ExcludePaths:         []string{"/healthz"},
		},

		Database: pulse.DatabaseConfig{
			SlowQueryThreshold: 100 * time.Millisecond,
			N1Threshold:        3,
		},

		Runtime: pulse.RuntimeConfig{
			SampleInterval: 3 * time.Second,
			LeakThreshold:  50,
		},

		Errors: pulse.ErrorConfig{
			MaxBodySize: 8192,
		},

		Health: pulse.HealthConfig{
			CheckInterval: 15 * time.Second,
			Timeout:       5 * time.Second,
		},

		Alerts: pulse.AlertConfig{
			Cooldown: 5 * time.Minute,
			Rules: []pulse.AlertRule{
				{
					Name:      "cart_slow",
					Metric:    "p95_latency",
					Operator:  ">",
					Threshold: 1000,
					Duration:  1 * time.Minute,
					Severity:  "warning",
				},
			},
			// Uncomment and set your Slack webhook URL to receive notifications:
			// Slack: &pulse.SlackConfig{
			// 	WebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
			// },
		},

		Prometheus: pulse.PrometheusConfig{
			Enabled: true,
			Path:    "/metrics",
		},
	})

	// Add custom health checks
	p.AddHealthCheck(pulse.HealthCheck{
		Name:     "external-api",
		Type:     "http",
		Critical: false,
		Timeout:  3 * time.Second,
		CheckFunc: func(ctx context.Context) error {
			req, _ := http.NewRequestWithContext(ctx, "GET", "https://httpbin.org/status/200", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("external API returned %d", resp.StatusCode)
			}
			return nil
		},
	})

	// Wrap external HTTP client for dependency monitoring
	paymentClient := pulse.WrapHTTPClient(p, &http.Client{
		Timeout: 10 * time.Second,
	}, "payment-gateway")

	// Application routes
	router.GET("/api/products", func(c *gin.Context) {
		var prods []Product
		db.Find(&prods)
		c.JSON(200, prods)
	})

	router.GET("/api/products/:id", func(c *gin.Context) {
		var prod Product
		if err := db.First(&prod, c.Param("id")).Error; err != nil {
			c.JSON(404, gin.H{"error": "product not found"})
			return
		}
		c.JSON(200, prod)
	})

	router.POST("/api/orders", func(c *gin.Context) {
		var order Order
		if err := c.ShouldBindJSON(&order); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		// Verify product exists
		var prod Product
		if err := db.First(&prod, order.ProductID).Error; err != nil {
			c.JSON(400, gin.H{"error": "invalid product_id"})
			return
		}

		order.CreatedAt = time.Now()
		db.Create(&order)
		c.JSON(201, order)
	})

	// Example endpoint that calls an external API (tracked by dependency monitoring)
	router.POST("/api/checkout", func(c *gin.Context) {
		req, _ := http.NewRequest("POST", "https://httpbin.org/post", nil)
		resp, err := paymentClient.Do(req)
		if err != nil {
			c.JSON(502, gin.H{"error": "payment gateway unavailable"})
			return
		}
		defer resp.Body.Close()

		c.JSON(200, gin.H{"status": "payment processed"})
	})

	// Example endpoint that panics (caught by Pulse error tracking)
	router.GET("/api/panic", func(c *gin.Context) {
		panic("something went terribly wrong!")
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting E-Commerce API on :%s", port)
	log.Printf("Dashboard:   http://localhost:%s/pulse/", port)
	log.Printf("Health:      http://localhost:%s/pulse/health", port)
	log.Printf("Prometheus:  http://localhost:%s/metrics", port)
	log.Printf("WebSocket:   ws://localhost:%s/pulse/ws/live", port)
	log.Printf("Login:       POST http://localhost:%s/pulse/api/auth/login", port)
	log.Printf("             {\"username\":\"admin\",\"password\":\"supersecret\"}")
	log.Fatal(router.Run(":" + port))
}
