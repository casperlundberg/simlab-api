// Command simlab-api serves the Simlab backend.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/casperlundberg/simlab-api/internal/app"
)

func main() {
	cfg, err := app.LoadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "simlab-api:", err)
		os.Exit(1)
	}

	// SIGTERM is how Kubernetes asks for a shutdown. Honouring it lets runs in
	// flight stop cleanly and clean up their autoscaler targets, rather than
	// leaving them behind for somebody to find later.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "simlab-api:", err)
		os.Exit(1)
	}
}
