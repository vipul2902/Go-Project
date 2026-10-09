// Command migrate applies database schema migrations.
//
// Usage: migrate [up|down|reset|status]   (default: up)
//
// It runs as a separate step before the API starts (a Compose one-off or a
// Kubernetes Job), never on API boot: with several replicas starting at
// once, boot-time migrations race each other.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vipul2902/clouddeploy/internal/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, url, database.PoolConfig{MaxConns: 2, MaxConnLifetime: time.Hour})
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool, command); err != nil {
		return err
	}
	fmt.Printf("migrate %s: ok\n", command)
	return nil
}
