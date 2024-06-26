package webtunnelserver

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/golang/glog"
)

const (
	ipStatusRequested = 1 // IP requested.
	ipStatusInUse     = 2 // IP in use.
)

// UserInfo represents the user information associated with an IP
type UserInfo struct {
	username, hostname string
	sessionStart       time.Time
}

// ipData represents data associated for each IP.
type ipData struct {
	ipStatus int
	data     any       // This field will point to the Websocket Connection object mapped to the IP
	userinfo *UserInfo // This field will be associated to the UserInfo object mapped to the IP
}

// IPPam represents a IP address mgmt struct
type IPPam struct {
	prefix      string
	allocations map[string]*ipData
	ip          net.IP
	ipnet       *net.IPNet
	net         net.IP
	bcast       net.IP
	lock        sync.Mutex
}

// NewIPPam returns a new IPPam object.
func NewIPPam(prefix string) (*IPPam, error) {

	ip, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return nil, err
	}

	// Get Network and broadcast addresses of prefix.
	bcast := lastAddr(ipnet)
	net := ip.Mask(ipnet.Mask)

	ippam := &IPPam{
		prefix:      prefix,
		allocations: make(map[string]*ipData),
		ip:          ip,
		ipnet:       ipnet,
		net:         net,
		bcast:       bcast,
	}

	// Allocate net and bcast addresses.
	ippam.allocations[bcast.String()] = &ipData{ipStatus: ipStatusInUse}
	ippam.allocations[net.String()] = &ipData{ipStatus: ipStatusInUse}

	return ippam, nil
}

// GetAllocatedCount returns the number of allocated IPs.
func (i *IPPam) GetAllocatedCount() int {
	return len(i.allocations)
}

// Check if an IP requested is valid in the network
func (i *IPPam) isValidIP(ipAddr string) bool {
	ip := net.ParseIP(ipAddr)
	if ip == nil {
		return false // Invalid format
	}
	return i.ipnet.Contains(ip)
}

// AcquireIP gets a free IP and marks the status as requested. SetIPactive should be called
// to make the IP active. data can be used to store any data associated with the IP.
func (i *IPPam) AcquireIP(data any) (string, error) {
	i.lock.Lock()
	defer i.lock.Unlock()

	for ip := i.ip.Mask(i.ipnet.Mask); i.ipnet.Contains(ip); inc(ip) {
		if _, exist := i.allocations[ip.String()]; !exist {
			i.allocations[ip.String()] = &ipData{
				ipStatus: ipStatusRequested,
				data:     data,
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("IPs exhausted")
}

// SetIPActiveWithUserInfo marks the IP as in use. IP is not considered active until this function is called.
// Also adds the username and hostname information associated with the IP connection.
func (i *IPPam) SetIPActiveWithUserInfo(ip, username, hostname string) error {
	i.lock.Lock()
	defer i.lock.Unlock()

	if _, exists := i.allocations[ip]; !exists {
		return fmt.Errorf("IP not available")
	}
	i.allocations[ip].ipStatus = ipStatusInUse
	i.allocations[ip].userinfo = &UserInfo{
		username:     username,
		hostname:     hostname,
		sessionStart: time.Now(),
	}
	return nil
}

// GetData returns the data associated with the IP.
func (i *IPPam) GetData(ip string) (any, error) {
	i.lock.Lock()
	defer i.lock.Unlock()

	if _, exists := i.allocations[ip]; !exists {
		return nil, fmt.Errorf("IP not allocated")
	}
	if v := i.allocations[ip]; v.ipStatus != ipStatusInUse {
		return nil, fmt.Errorf("IP not marked in use")
	}
	return i.allocations[ip].data, nil
}

// GetUserinfo returns the UnserInfo associated with the IP.
func (i *IPPam) GetUserinfo(ip string) (UserInfo, error) {
	i.lock.Lock()
	defer i.lock.Unlock()

	if v, exists := i.allocations[ip]; !exists || v.ipStatus != ipStatusInUse {
		return UserInfo{}, fmt.Errorf("IP not available or not marked in use")
	}
	return *i.allocations[ip].userinfo, nil
}

// ReleaseIP returns IP address back to pool.
func (i *IPPam) ReleaseIP(ip string) error {
	i.lock.Lock()
	defer i.lock.Unlock()

	if i.net.String() == ip || i.bcast.String() == ip {
		return fmt.Errorf("cannot release network or broadcast address")
	}
	if _, exists := i.allocations[ip]; !exists {
		return fmt.Errorf("IP not allocated")
	}
	delete(i.allocations, ip)
	return nil
}

// DumpAllocations returns the current IP mapping and user information
func (i *IPPam) DumpAllocations() map[string]*UserInfo {
	i.lock.Lock()
	defer i.lock.Unlock()
	allocations := make(map[string]*UserInfo)
	for k, v := range i.allocations {
		d := v.userinfo
		if d == nil {
			continue
		}
		allocations[k] = d
	}
	return allocations
}

// AcquireSpecificIP acquires specific IP and marks it as in use.
func (i *IPPam) AcquireSpecificIP(ip string, data any) error {
	if ok := i.isValidIP(ip); !ok {
		return fmt.Errorf("not a valid IP: %v", ip)
	}
	i.lock.Lock()
	defer i.lock.Unlock()

	if _, exists := i.allocations[ip]; exists {
		return fmt.Errorf("IP already in use")
	}
	i.allocations[ip] = &ipData{
		data:     data,
		ipStatus: ipStatusInUse,
	}
	return nil
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

// inc increments an IP address
func lastAddr(n *net.IPNet) net.IP {
	ip := make(net.IP, len(n.IP.To4()))
	binary.BigEndian.PutUint32(ip, binary.BigEndian.Uint32(n.IP.To4())|^binary.BigEndian.Uint32(net.IP(n.Mask).To4()))
	return ip
}

// Returns the maximum number associated with a CIDR
func getMaxUsers(clientNetPrefix string) int {

	_, ipnet, err := net.ParseCIDR(clientNetPrefix)
	if err != nil {
		glog.Fatal("Could not parse Client CIDR")
	}

	// Gateway will reject requests when the user count reaches 95%.
	size, _ := ipnet.Mask.Size()
	max := math.Pow(2, float64(32-size)) - 3 // router,network,broadcast allocations have to be remove from the count
	if max < 0 {
		max = 0
	}
	return int(max)
}
