package main

import (
	"context"
	"log"

	"james/moneypenny/pkg/channel"
)

func main() {
	if err := channel.RunAgencyPluginFromEnv(context.Background()); err != nil {
		log.Fatal(err)
	}
}
