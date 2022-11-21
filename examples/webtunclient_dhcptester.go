// webtunclient.go - Example client implementation.
package main

import (
	"crypto/tls"
	"flag"
	"os"
	"os/signal"
	"syscall"

	//"github.com/deepakkamesh/webtunnel/webtunnelclient"
	"webtunnel/webtunnelclient"

	"github.com/golang/glog"
	"github.com/gorilla/websocket"
)

// InitializeOS assigns IP to tunnel and sets up routing via tunnel.
func InitializeOS(cfg *webtunnelclient.Interface) error {
	return nil
}

func main() {
	flag.Parse()
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// Initialize and Startup Webtunnel.
	glog.Warning("Starting WebTunnel...")

	// Create a dialer with options.
	wsDialer := websocket.Dialer{}
	wsDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	// Initialize the client.
	client, err := webtunnelclient.NewWebtunnelClient("169.254.114.28:8811", &wsDialer,
		// linux: false, InitializeOS, true, 1600)
		true, InitializeOS, true, 3600)
	if err != nil {
		glog.Exitf("Failed to initialize client: %s", err)
	}

	// Start the client.
	if err := client.StartDHCPTest(); err != nil {
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
