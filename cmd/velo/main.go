package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"velo/internal/app"
)

// version, commit, and date are stamped at build time:
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234 -X main.date=2026-11-21T00:00:00Z"
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a, err := app.New(app.Options{
		Version: version,
		Commit:  commit,
		Date:    date,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Context: ctx,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "velo: %v\n", err)
		os.Exit(1)
	}

	if err := a.Execute(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "velo: %v\n", err)
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		os.Exit(1)
	}
}
