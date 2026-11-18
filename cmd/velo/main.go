package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"velo/internal/app"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a, err := app.New(app.Options{
		Version: version,
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
