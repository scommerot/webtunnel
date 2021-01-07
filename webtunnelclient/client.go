/* Package client runs the client side of the webtunnel. It establishes the
* the tunnel using a network interface.
 */
package webtunnelclient

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"time"

	wc "github.com/deepakkamesh/webtunnel/webtunnelcommon"
	"github.com/golang/glog"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/gorilla/websocket"
	"github.com/songgao/water"
)

// Interface represents the network interface.
type Interface struct {
	IP           net.IP           // IP address.
	GWIP         net.IP           // Gateway IP.
	Netmask      net.IP           // Netmask of the interface.
	DNS          []net.IP         // IP of DNS servers.
	RoutePrefix  []*net.IPNet     // Route prefix to send via tunnel.
	localHWAddr  net.HardwareAddr // MAC address of network interface.
	gwHWAddr     net.HardwareAddr // fake MAC address of gateway.
	leaseTime    uint32           // DHCP lease time.
	wc.Interface                  // Interface to network.
}

// WebtunnelClient represents the client struct.
type WebtunnelClient struct {
	wsconn       *websocket.Conn        // Websocket connection.
	Error        chan error             // Channel to handle errors from goroutines.
	ifce         *Interface             // Struct to hold interface configuration.
	userInitFunc func(*Interface) error // User supplied callback for OS initialization.
}

// Overrides for testing.
var newWaterInterface = func(c water.Config) (wc.Interface, error) {
	return water.New(c)
}

// NewWebTunnelClient returns an initialized webtunnel client.
func NewWebtunnelClient(serverIPPort string, wsDialer *websocket.Dialer,
	devType water.DeviceType, f func(*Interface) error, secure bool) (*WebtunnelClient, error) {

	// Initialize websocket connection.
	scheme := "ws"
	if secure {
		scheme = "wss"
	}
	u := url.URL{Scheme: scheme, Host: serverIPPort, Path: "/ws"}
	wsconn, _, err := wsDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, err
	}

	// Initialize network interface.
	handle, err := newWaterInterface(water.Config{
		DeviceType: devType,
	})
	if err != nil {
		return nil, fmt.Errorf("error creating int %s", err)
	}

	return &WebtunnelClient{
		wsconn: wsconn,
		Error:  make(chan error),
		ifce: &Interface{
			Interface: handle,
		},
		userInitFunc: f,
	}, nil
}

func (w *WebtunnelClient) Start() error {

	err := w.configureInterface()
	if err != nil {
		return err
	}
	go w.processNetPacket()
	go w.processWSPacket()

	return nil
}

// configureInterface retrieves the client configuration from server and sends to Net daemon.
func (w *WebtunnelClient) configureInterface() error {
	// Get configuration from server.
	if err := w.wsconn.WriteMessage(websocket.TextMessage, []byte("getConfig")); err != nil {
		return err
	}
	cfg := &wc.ClientConfig{}
	if err := w.wsconn.ReadJSON(cfg); err != nil {
		return err
	}
	glog.V(1).Infof("Retrieved config from server %v", *cfg)

	var dnsIPs []net.IP
	for _, v := range cfg.DNS {
		dnsIPs = append(dnsIPs, net.ParseIP(v).To4())
	}
	var routes []*net.IPNet
	for _, v := range cfg.RoutePrefix {
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			return err
		}
		routes = append(routes, n)
	}
	w.ifce.IP = net.ParseIP(cfg.Ip).To4()
	w.ifce.GWIP = net.ParseIP(cfg.GWIp).To4()
	w.ifce.Netmask = net.ParseIP(cfg.Netmask).To4()
	w.ifce.DNS = dnsIPs
	w.ifce.RoutePrefix = routes
	w.ifce.gwHWAddr = wc.GenMACAddr()
	w.ifce.leaseTime = 300

	// Call user supplied function for any OS initializations needed from cli.
	// Depending on OS this might be bringing up OS or other network commands.
	if err := w.userInitFunc(w.ifce); err != nil {
		return err
	}

	return nil
}

// Stop gracefully shutdowns the client after notifying the server.
func (w *WebtunnelClient) Stop() error {
	err := w.wsconn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		return err
	}
	// Wait for some time for server to terminate conn before closing on client end.
	// Otherwise its seen as a abnormal closure and will result in error.
	time.Sleep(time.Second)
	w.wsconn.Close()
	return nil
}

// processWSPacket processes packets received from the Websocket connection and
// writes to the network interface.
func (w *WebtunnelClient) processWSPacket() {

	// Wait for tap/tun interface configuration to be complete by DHCP(TAP) or manual (TUN).
	// Otherwise writing to network interface will fail.
	for !wc.IsConfigured(w.ifce.Name(), w.ifce.IP.String()) {
		time.Sleep(2 * time.Second)
		glog.V(1).Infof("Waiting for interface to be ready...")
	}
	// get the localHW addr only after network interface is configured.
	w.ifce.localHWAddr = wc.GetMacbyName(w.ifce.Name())
	glog.V(1).Infof("Interface Ready.")

	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}

	for {
		// Read packet from websocket.
		mt, pkt, err := w.wsconn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return
			}
			w.Error <- fmt.Errorf("error reading websocket %s", err)
			return
		}
		if mt != websocket.BinaryMessage {
			glog.Warningf("Binary message type recvd from websocket")
			continue
		}
		wc.PrintPacketIPv4(pkt, "Client <- WebSocket")

		// Wrap packet in Ethernet header before sending if TAP.
		if w.ifce.IsTAP() {
			packet := gopacket.NewPacket(pkt, layers.LayerTypeIPv4, gopacket.Default)
			ipv4 := packet.Layer(layers.LayerTypeIPv4).(*layers.IPv4)

			ethl := &layers.Ethernet{
				SrcMAC:       w.ifce.gwHWAddr,
				DstMAC:       w.ifce.localHWAddr,
				EthernetType: layers.EthernetTypeIPv4,
			}
			buffer := gopacket.NewSerializeBuffer()
			if err := gopacket.SerializeLayers(buffer, opts, ethl, ipv4, gopacket.Payload(ipv4.Payload)); err != nil {
				glog.Errorf("error serializelayer %s", err)
			}
			pkt = buffer.Bytes()
		}

		// Send packet to network interface.
		if _, err := w.ifce.Write(pkt); err != nil {
			w.Error <- fmt.Errorf("error writing to tunnel %s.", err)
			return
		}

	}
}

// processNetPacket processes the packet from the network interface and dispatches
// to the websocket connection.
func (w *WebtunnelClient) processNetPacket() {
	pkt := make([]byte, 2048)
	var oPkt []byte

	for {
		// Read from TUN/TAP network interface.
		n, err := w.ifce.Read(pkt)
		if err != nil {
			w.Error <- fmt.Errorf("error reading Tunnel %s. Sz:%v", err, n)
			return
		}
		oPkt = pkt

		// Special handling for TAP; ARP/DHCP.
		if w.ifce.IsTAP() {
			packet := gopacket.NewPacket(pkt, layers.LayerTypeEthernet, gopacket.Default)
			if _, ok := packet.Layer(layers.LayerTypeARP).(*layers.ARP); ok {
				if err := w.handleArp(packet); err != nil {
					w.Error <- fmt.Errorf("err sending arp %v", err)
				}
				continue
			}
			if _, ok := packet.Layer(layers.LayerTypeDHCPv4).(*layers.DHCPv4); ok {
				if err := w.handleDHCP(packet); err != nil {
					w.Error <- fmt.Errorf("err sending dhcp  %v", err)
				}
				continue
			}
			// Only send IPv4 unicast packets to reduce noisy windows machines.
			ipv4, ok := packet.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
			if !ok || ipv4.DstIP.IsMulticast() {
				continue
			}
			// Strip Ethernet header and send.
			oPkt = packet.Layer(layers.LayerTypeEthernet).(*layers.Ethernet).LayerPayload()
		}

		wc.PrintPacketIPv4(pkt, "Client  -> Websocket")
		if err := w.wsconn.WriteMessage(websocket.BinaryMessage, oPkt); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return
			}
			w.Error <- fmt.Errorf("error writing to websocket: %s", err)
			return
		}
	}
}

// buildDHCPopts builds the options for DHCP Response.
func (w *WebtunnelClient) buildDHCPopts(leaseTime uint32, msgType layers.DHCPMsgType) layers.DHCPOptions {
	var opt []layers.DHCPOption
	tm := make([]byte, 4)
	binary.BigEndian.PutUint32(tm, leaseTime)

	for _, s := range w.ifce.DNS {
		opt = append(opt, layers.NewDHCPOption(layers.DHCPOptDNS, s))
	}
	opt = append(opt, layers.NewDHCPOption(layers.DHCPOptSubnetMask, w.ifce.Netmask))
	opt = append(opt, layers.NewDHCPOption(layers.DHCPOptLeaseTime, tm))
	opt = append(opt, layers.NewDHCPOption(layers.DHCPOptMessageType, []byte{byte(msgType)}))
	opt = append(opt, layers.NewDHCPOption(layers.DHCPOptServerID, w.ifce.GWIP))

	// Construct the classless static route.
	// format: {size of netmask, <route prefix>, <gateway> ...}
	// The size of netmask dictates how to read the route prefix. (eg. 24 - read next 3 bytes or 25 read next 4 bytes)
	var route []byte
	for _, n := range w.ifce.RoutePrefix {
		netAddr := []byte(n.IP.To4())
		mask, _ := n.Mask.Size()
		b := mask / 8
		if mask%8 > 0 {
			b++
		}
		// Add only the size of netmask.
		netAddr = netAddr[:b]
		route = append(route, byte(mask))     // Add netmask size.
		route = append(route, netAddr...)     // Add network.
		route = append(route, w.ifce.GWIP...) // Add gateway.
	}
	opt = append(opt, layers.NewDHCPOption(layers.DHCPOptClasslessStaticRoute, route))

	return opt
}

// handleDHCP handles the DHCP requests from kernel.
func (w *WebtunnelClient) handleDHCP(packet gopacket.Packet) error {

	dhcp := packet.Layer(layers.LayerTypeDHCPv4).(*layers.DHCPv4)
	udp := packet.Layer(layers.LayerTypeUDP).(*layers.UDP)
	ipv4 := packet.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
	eth := packet.Layer(layers.LayerTypeEthernet).(*layers.Ethernet)

	// Get the DHCP Message Type.
	var msgType layers.DHCPMsgType
	for _, v := range dhcp.Options {
		if v.Type == layers.DHCPOptMessageType {
			msgType = layers.DHCPMsgType(v.Data[0])
		}
	}

	var dhcpl = &layers.DHCPv4{
		Operation:    layers.DHCPOpReply,
		HardwareType: layers.LinkTypeEthernet,
		HardwareLen:  dhcp.HardwareLen,
		Xid:          dhcp.Xid,
		YourClientIP: w.ifce.IP,
		NextServerIP: w.ifce.GWIP,
		ClientHWAddr: eth.SrcMAC,
	}

	switch msgType {
	case layers.DHCPMsgTypeDiscover:
		dhcpl.Options = w.buildDHCPopts(w.ifce.leaseTime, layers.DHCPMsgTypeOffer)

	case layers.DHCPMsgTypeRequest:
		dhcpl.Options = w.buildDHCPopts(w.ifce.leaseTime, layers.DHCPMsgTypeAck)

	case layers.DHCPMsgTypeRelease:
		glog.Warningf("Got an IP release request. Unexpected.")
	}

	// Construct and send DHCP Packet.
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	ethl := &layers.Ethernet{
		SrcMAC:       w.ifce.gwHWAddr,
		DstMAC:       net.HardwareAddr{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		EthernetType: layers.EthernetTypeIPv4,
	}
	ipv4l := &layers.IPv4{
		Version:  ipv4.Version,
		TTL:      ipv4.TTL,
		SrcIP:    w.ifce.GWIP,
		DstIP:    net.IP{255, 255, 255, 255},
		Protocol: layers.IPProtocolUDP,
	}
	udpl := &layers.UDP{
		SrcPort: udp.DstPort,
		DstPort: udp.SrcPort,
	}
	if err := udpl.SetNetworkLayerForChecksum(ipv4l); err != nil {
		return fmt.Errorf("error checksum %s", err)
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, opts, ethl, ipv4l, udpl, dhcpl); err != nil {
		return fmt.Errorf("error serializelayer %s", err)
	}
	wc.PrintPacketEth(buffer.Bytes(), "DHCP Reply")
	if _, err := w.ifce.Write(buffer.Bytes()); err != nil {
		return err
	}

	return nil
}

// handleArp handles the ARPs requests via the TAP interface. All responses are
// sent the virtual MAC HWAddr for gateway.
func (w *WebtunnelClient) handleArp(packet gopacket.Packet) error {

	arp := packet.Layer(layers.LayerTypeARP).(*layers.ARP)
	eth := packet.Layer(layers.LayerTypeEthernet).(*layers.Ethernet)

	if arp.Operation != layers.ARPRequest {
		return nil
	}

	// Construct and send ARP response.
	arpl := &layers.ARP{
		AddrType:          arp.AddrType,
		Protocol:          arp.Protocol,
		HwAddressSize:     arp.HwAddressSize,
		ProtAddressSize:   arp.ProtAddressSize,
		Operation:         layers.ARPReply,
		SourceHwAddress:   w.ifce.gwHWAddr,
		SourceProtAddress: arp.DstProtAddress,
		DstHwAddress:      arp.SourceHwAddress,
		DstProtAddress:    arp.SourceProtAddress,
	}
	ethl := &layers.Ethernet{
		SrcMAC:       w.ifce.gwHWAddr,
		DstMAC:       eth.SrcMAC,
		EthernetType: layers.EthernetTypeARP,
	}

	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, opts, ethl, arpl); err != nil {
		return fmt.Errorf("error Serializelayer %s", err)
	}
	wc.PrintPacketEth(buffer.Bytes(), "ARP Response")
	if _, err := w.ifce.Write(buffer.Bytes()); err != nil {
		return err
	}
	return nil
}