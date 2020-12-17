package main

import (
	"flag"

	"github.com/deepakkamesh/webtunnel/webtunnelserver"
	"github.com/golang/glog"
)

func main() {
	// Get some flags.
	listenAddr := flag.String("listenAddr", ":8811", "Bind address:port")
	httpsKeyFile := flag.String("httpsKeyFile", "localhost.key", "HTTPS Key file path")
	httpsCertFile := flag.String("httpsCertFile", "localhost.crt", "HTTPS Cert file path")

	flag.Parse()

	routePrefix := []string{"172.16.0.1/32", "172.16.0.2/32"}
	glog.Info("starting webtunnel server..")
	server, err := webtunnelserver.NewWebTunnelServer(*listenAddr, "192.168.0.1",
		"255.255.255.0", "192.168.0.0/24", routePrefix, *httpsKeyFile, *httpsCertFile)
	if err != nil {
		glog.Fatalf("%s", err)
	}
	server.Start()

	glog.Info("starting DNS Forwarder..")
	dns, err := webtunnelserver.NewDNSForwarder("192.168.0.1", 53)
	if err != nil {
		glog.Fatal(err)
	}
	dns.Start()

	select {
	case err := <-server.Error:
		glog.Exitf("Shutting down server %v", err)
	}
}
