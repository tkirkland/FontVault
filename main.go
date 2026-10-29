// Command fontvault archives TTF/OTF fonts in a GitHub repository via Git
// LFS and installs repository fonts that are missing locally.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"fontvault/internal/app"
	"fontvault/internal/cli"
)

// version is injected at build time: -X main.version=<tag>.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := cli.Parse(args, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fontvault: %v\nTry 'fontvault --help' for usage.\n", err)
		return app.ExitUsage
	}
	if opts.Help {
		cli.Usage(os.Stdout)
		return app.ExitOK
	}
	if opts.Version {
		fmt.Println(version)
		return app.ExitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := app.New(opts, version, os.Stdin, os.Stdout, os.Stderr)
	return a.Run(ctx)
}
