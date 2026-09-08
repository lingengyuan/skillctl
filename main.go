package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/lingengyuan/skillctl/internal/app"
)

var version = "0.0.4"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := app.Run(ctx, os.Args[1:], version, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
