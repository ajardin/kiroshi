// Command kiroshi is the kiroshi CLI entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ajardin/kiroshi/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	if !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "kiroshi:", err)
	}
	os.Exit(1)
}
