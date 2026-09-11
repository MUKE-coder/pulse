// Command multi-instance runs one API replica with Pulse, configured the way
// every replica behind a load balancer should be:
//
//   - one JWT signing key shared by all replicas (PULSE_SECRET), so a
//     dashboard login works whichever replica serves the next request;
//   - a per-replica instance ID (INSTANCE_ID, default: the hostname);
//   - per-replica storage (PULSE_DB). Each replica's dashboard shows only its
//     own traffic, so the load balancer pins each browser to one replica.
//
// docker-compose.yml in this directory runs two replicas behind Caddy.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MUKE-coder/pulse/pulse"
	"github.com/gin-gonic/gin"
)

func main() {
	secret := os.Getenv("PULSE_SECRET")
	if secret == "" {
		log.Fatal("PULSE_SECRET must be set, to the same value on every replica")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := gin.Default()
	p := pulse.Mount(ctx, router, nil,
		pulse.WithAppName("multi-instance-demo"),
		pulse.WithCredentials(os.Getenv("PULSE_USER"), os.Getenv("PULSE_PASSWORD")),
		pulse.WithSecretKey(secret),
		pulse.WithInstanceID(os.Getenv("INSTANCE_ID")),
		pulse.WithSQLite(os.Getenv("PULSE_DB")),
	)

	router.GET("/api/hello", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"served_by": p.GetConfig().InstanceID})
	})

	srv := &http.Server{Addr: ":8080", Handler: router}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if err := p.Shutdown(); err != nil {
		log.Printf("pulse shutdown: %v", err)
	}
}
