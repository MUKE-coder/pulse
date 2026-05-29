package main

import (
	"context"
	"log"

	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func main() {
	// Initialize database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	// Initialize Gin router
	router := gin.Default()

	// Mount Pulse — Pulse's background goroutines exit when ctx is canceled.
	ctx := context.Background()
	_ = pulse.Mount(ctx, router, db,
		pulse.WithAppName("Blog API"),
		pulse.WithDevMode(),
	)

	// Sample API routes
	router.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "Welcome to the Blog API"})
	})
	router.GET("/api/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	log.Println("Starting server on :8080")
	if err := router.Run(":8080"); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
