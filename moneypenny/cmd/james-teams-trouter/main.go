package main

import (
	"context"
	"flag"
	"log"
	"os"

	"james/moneypenny/pkg/channel"
)

var Version = "dev"

func main() {
	dryRun := flag.Bool("dry-run", false, "run without Teams authentication or network access")
	flag.Parse()
	if err := channel.RunTrouterPluginFromEnv(context.Background(), os.Stdin, os.Stdout, *dryRun); err != nil {
		log.Fatal(err)
	}
}
