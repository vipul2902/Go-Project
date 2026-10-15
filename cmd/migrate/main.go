// Command migrate applies database schema migrations.
//
// Usage: migrate [command...]   (default: up)
//
//	up | down | reset | status   schema migrations (as the schema owner)
//	app-user                     create or update the application's login role
//	                             (APP_DB_USER, default clouddeploy_api, with
//	                             APP_DB_PASSWORD) as a member of clouddeploy_app
//
// Commands run in order, e.g. `migrate up app-user` in the Kubernetes Job.
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
	commands := os.Args[1:]
	if len(commands) == 0 {
		commands = []string{"up"}
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

	for _, command := range commands {
		switch command {
		case "app-user":
			user := os.Getenv("APP_DB_USER")
			if user == "" {
				user = "clouddeploy_api"
			}
			if err := database.EnsureAppUser(ctx, pool, user, os.Getenv("APP_DB_PASSWORD")); err != nil {
				return err
			}
			fmt.Printf("migrate app-user: role %s ok\n", user)
		default:
			if err := database.Migrate(ctx, pool, command); err != nil {
				return err
			}
			fmt.Printf("migrate %s: ok\n", command)
		}
	}
	return nil
}
