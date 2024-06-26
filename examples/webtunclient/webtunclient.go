// webtunclient.go - Example client implementation.
package main

import (
	"crypto/tls"
	"flag"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/deepakkamesh/webtunnel/webtunnelclient"
	"github.com/golang/glog"
	"github.com/gorilla/websocket"
)

var webtunServer = flag.String("webtunServer", "192.168.1.117:8811", "IP:PORT of webtunnel server")

func main() {
	flag.Parse()
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	// Initialize and Startup Webtunnel.
	glog.Warning("Starting WebTunnel...")

	// Create a dialer with options and support of Proxy Environment
	wsDialer := *websocket.DefaultDialer
	wsDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	// Initialize the client.
	isTap := false
	var leaseTime uint32 = 300
	if runtime.GOOS == "windows" {
		isTap = true
		leaseTime = 3000
	}
	client, err := webtunnelclient.NewWebtunnelClient(*webtunServer, &wsDialer,
		isTap, InitializeOS, true, leaseTime)
	if err != nil {
		glog.Exitf("Failed to initialize client: %s", err)
	}
	clientPlatformSpecifics(client)

	// Start the client.
	if err := client.Start(); err != nil {
		glog.Exit(err)
	}

	select {
	case <-c:
		client.Stop()
		glog.Infoln("Shutting down WebTunnel")

	// client.Error channel returns errors that may be unrecoverable. The user can decide how to handle them.
	case err := <-client.Error:
		glog.Exitf("Client failure: %s", err)
	}
}
