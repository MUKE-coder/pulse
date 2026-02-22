package main

import (
	"log"

	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// User is a sample GORM model.
type User struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `json:"name"`
}

func main() {
	// Initialize database
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	db.AutoMigrate(&User{})

	// Seed data
	db.Create(&User{Name: "Alice"})
	db.Create(&User{Name: "Bob"})

	// Initialize Gin router
	router := gin.Default()

	// Mount Pulse — that's it!
	pulse.Mount(router, db, pulse.Config{
		AppName: "Basic Example",
		DevMode: true,
	})

	// Application routes
	router.GET("/api/users", func(c *gin.Context) {
		var users []User
		db.Find(&users)
		c.JSON(200, users)
	})

	router.GET("/api/users/:id", func(c *gin.Context) {
		var user User
		if err := db.First(&user, c.Param("id")).Error; err != nil {
			c.JSON(404, gin.H{"error": "user not found"})
			return
		}
		c.JSON(200, user)
	})

	router.POST("/api/users", func(c *gin.Context) {
		var user User
		if err := c.ShouldBindJSON(&user); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		db.Create(&user)
		c.JSON(201, user)
	})

	// Start server
	//
	// Dashboard:  http://localhost:8080/pulse/
	// Health:     http://localhost:8080/pulse/health
	// API:        http://localhost:8080/pulse/api/overview
	//             (login first: POST /pulse/api/auth/login with {"username":"admin","password":"pulse"})
	log.Println("Starting server on :8080")
	log.Println("Dashboard:  http://localhost:8080/pulse/")
	log.Println("Health:     http://localhost:8080/pulse/health")
	log.Fatal(router.Run(":8080"))
}
