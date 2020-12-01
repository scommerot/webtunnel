package main

import (
	"fmt"
	"log"

	"github.com/deepakkamesh/webtunnel/webtunnelclient"
)

func main() {

	fmt.Println("Starting WebTunnel...")

	fmt.Println("Initialization Complete.")

	client, err := webtunnelclient.NewWebtunnelClient("192.168.1.117:8811")
	if err != nil {
		log.Fatalf("err %s", err)
	}
	client.Start()
	for {
	}
}
