package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sabbasth/tidy-wow/internal/cli"
)

var version = "dev"

func main() {
	app := cli.New(version, os.Stdin, os.Stdout, os.Stderr)
	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tidy-wow:", err)
		os.Exit(1)
	}
}
