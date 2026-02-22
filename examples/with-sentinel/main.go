package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// This example demonstrates Pulse as a production sentinel:
// - Multiple health checks with critical/non-critical distinction
// - Aggressive alerting rules
// - Multi-channel notifications
// - Dependency monitoring for external services
// - Simulated traffic to generate realistic metrics

type Task struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func main() {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatal(err)
	}
	db.AutoMigrate(&Task{})

	router := gin.Default()

	p := pulse.Mount(router, db, pulse.Config{
		AppName: "Sentinel Example",
		DevMode: true,

		Dashboard: pulse.DashboardConfig{
			Username: "sentinel",
			Password: "watchdog",
		},

		Tracing: pulse.TracingConfig{
			SlowRequestThreshold: 200 * time.Millisecond,
		},

		Database: pulse.DatabaseConfig{
			SlowQueryThreshold: 50 * time.Millisecond,
			N1Threshold:        3,
		},

		Health: pulse.HealthConfig{
			CheckInterval: 10 * time.Second,
			Timeout:       3 * time.Second,
		},

		Alerts: pulse.AlertConfig{
			Cooldown: 2 * time.Minute,
			Rules: []pulse.AlertRule{
				{
					Name:      "high_latency",
					Metric:    "p95_latency",
					Operator:  ">",
					Threshold: 500,
					Duration:  1 * time.Minute,
					Severity:  "critical",
				},
				{
					Name:      "error_spike",
					Metric:    "error_rate",
					Operator:  ">",
					Threshold: 5,
					Duration:  1 * time.Minute,
					Severity:  "warning",
				},
				{
					Name:      "memory_warning",
					Metric:    "heap_alloc_mb",
					Operator:  ">",
					Threshold: 100,
					Duration:  2 * time.Minute,
					Severity:  "warning",
				},
			},

			// Uncomment to enable notifications:
			//
			// Slack: &pulse.SlackConfig{
			// 	WebhookURL: os.Getenv("SLACK_WEBHOOK_URL"),
			// },
			//
			// Discord: &pulse.DiscordConfig{
			// 	WebhookURL: os.Getenv("DISCORD_WEBHOOK_URL"),
			// },
			//
			// Webhooks: []pulse.WebhookConfig{
			// 	{
			// 		URL:    "https://your-webhook-endpoint.com/alerts",
			// 		Secret: "your-hmac-secret",
			// 	},
			// },
		},

		Prometheus: pulse.PrometheusConfig{
			Enabled: true,
		},
	})

	// Register multiple health checks
	p.AddHealthCheck(pulse.HealthCheck{
		Name:     "cache",
		Type:     "redis",
		Critical: true,
		CheckFunc: func(ctx context.Context) error {
			// Simulate a cache check — replace with real Redis ping
			time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			return nil
		},
	})

	p.AddHealthCheck(pulse.HealthCheck{
		Name:     "search-engine",
		Type:     "http",
		Critical: false, // Non-critical — won't make /ready fail
		CheckFunc: func(ctx context.Context) error {
			time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
			// Occasionally fail to demonstrate degraded status
			if rand.Float32() < 0.05 {
				return fmt.Errorf("search engine timeout")
			}
			return nil
		},
	})

	p.AddHealthCheck(pulse.HealthCheck{
		Name:     "storage",
		Type:     "disk",
		Critical: true,
		CheckFunc: func(ctx context.Context) error {
			time.Sleep(time.Duration(rand.Intn(3)) * time.Millisecond)
			return nil
		},
	})

	// Wrap external HTTP client for dependency monitoring
	externalAPI := pulse.WrapHTTPClient(p, &http.Client{
		Timeout: 5 * time.Second,
	}, "external-api")

	notificationService := pulse.WrapHTTPClient(p, &http.Client{
		Timeout: 3 * time.Second,
	}, "notification-service")

	// Application routes
	router.GET("/api/tasks", func(c *gin.Context) {
		var tasks []Task
		db.Find(&tasks)
		c.JSON(200, tasks)
	})

	router.POST("/api/tasks", func(c *gin.Context) {
		var task Task
		if err := c.ShouldBindJSON(&task); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		task.CreatedAt = time.Now()
		task.Status = "pending"
		db.Create(&task)
		c.JSON(201, task)
	})

	router.GET("/api/slow", func(c *gin.Context) {
		// Simulate slow endpoint
		time.Sleep(time.Duration(200+rand.Intn(300)) * time.Millisecond)
		c.JSON(200, gin.H{"message": "slow response"})
	})

	router.GET("/api/error", func(c *gin.Context) {
		c.JSON(500, gin.H{"error": "simulated error"})
	})

	// Start simulated traffic in background
	go simulateTraffic(externalAPI, notificationService)

	log.Println("Starting Sentinel Example on :8080")
	log.Println("Dashboard:   http://localhost:8080/pulse/")
	log.Println("Health:      http://localhost:8080/pulse/health")
	log.Println("Prometheus:  http://localhost:8080/pulse/metrics")
	log.Println("Login:       POST /pulse/api/auth/login {\"username\":\"sentinel\",\"password\":\"watchdog\"}")
	log.Fatal(router.Run(":8080"))
}

// simulateTraffic generates background HTTP requests to produce realistic metrics.
func simulateTraffic(externalAPI, notificationService *http.Client) {
	// Wait for server to start
	time.Sleep(2 * time.Second)

	var wg sync.WaitGroup
	client := &http.Client{Timeout: 5 * time.Second}

	// Simulate normal API traffic
	wg.Add(1)
	go func() {
		defer wg.Done()
		endpoints := []string{
			"http://localhost:8080/api/tasks",
			"http://localhost:8080/api/tasks",
			"http://localhost:8080/api/tasks",
			"http://localhost:8080/api/slow",
		}
		for {
			url := endpoints[rand.Intn(len(endpoints))]
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
			}
			time.Sleep(time.Duration(100+rand.Intn(400)) * time.Millisecond)
		}
	}()

	// Simulate occasional errors
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if rand.Float32() < 0.1 {
				resp, err := client.Get("http://localhost:8080/api/error")
				if err == nil {
					resp.Body.Close()
				}
			}
			time.Sleep(time.Duration(500+rand.Intn(2000)) * time.Millisecond)
		}
	}()

	// Simulate external API calls (tracked by dependency monitoring)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			resp, err := externalAPI.Get("https://httpbin.org/delay/0")
			if err == nil {
				resp.Body.Close()
			}
			time.Sleep(time.Duration(2000+rand.Intn(3000)) * time.Millisecond)
		}
	}()

	// Simulate notification service calls
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			resp, err := notificationService.Get("https://httpbin.org/status/200")
			if err == nil {
				resp.Body.Close()
			}
			time.Sleep(time.Duration(5000+rand.Intn(5000)) * time.Millisecond)
		}
	}()

	wg.Wait()
}
