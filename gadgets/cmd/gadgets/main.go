package main

import (
	"os"

	"james/gadgets/internal/cli"
)

var Version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv, Version))
}
