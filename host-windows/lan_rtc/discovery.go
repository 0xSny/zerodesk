package lan_rtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"zerodesk-host/logger"
)

const DiscoverMagic = "ZERODESK_DISCOVER"

type DiscoveryReply struct {
	Service       string   `json:"service"`
	Version       string   `json:"version"`
	Hostname      string   `json:"hostname"`
	IPs           []string `json:"ips"`
	PrimaryIP     string   `json:"primary_ip"`
	MacAddress    string   `json:"mac_address"`
	HTTPPort      int      `json:"http_port"`
	DiscoveryPort int      `json:"discovery_port"`
}

// IsPrivateLANIPv4 returns true only for RFC1918 private LAN IPv4 addresses (excluding loopback & link-local 169.254.x.x).
func IsPrivateLANIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() || ip4.IsMulticast() {
		return false
	}
	// 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
	if ip4[0] == 10 {
		return true
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	return false
}

// GetLANIPs returns sorted private LAN IPv4 addresses, prioritizing non-virtual adapters first.
func GetLANIPs() []string {
	return GetFilteredLANIPs(false)
}

// GetFilteredLANIPs returns private LAN IPv4 addresses; if filterVirtual is true and at least one
// physical LAN adapter exists, virtual adapters (VMware/WSL/Hyper-V/VBox/Docker) are excluded.
func GetFilteredLANIPs(filterVirtual bool) []string {
	type ifaceIP struct {
		ip        string
		isVirtual bool
	}
	var physical []ifaceIP
	var virtual []ifaceIP
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		nameLower := strings.ToLower(iface.Name)
		isVirt := strings.Contains(nameLower, "vmware") ||
			strings.Contains(nameLower, "virtual") ||
			strings.Contains(nameLower, "vethernet") ||
			strings.Contains(nameLower, "hyper-v") ||
			strings.Contains(nameLower, "vbox") ||
			strings.Contains(nameLower, "wsl") ||
			strings.Contains(nameLower, "docker")

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if IsPrivateLANIPv4(ip) {
				entry := ifaceIP{ip: ip.To4().String(), isVirtual: isVirt}
				if isVirt {
					virtual = append(virtual, entry)
				} else {
					physical = append(physical, entry)
				}
			}
		}
	}

	list := physical
	if !filterVirtual || len(physical) == 0 {
		list = append(list, virtual...)
	}

	sort.SliceStable(list, func(i, j int) bool {
		if list[i].isVirtual != list[j].isVirtual {
			return !list[i].isVirtual
		}
		return list[i].ip < list[j].ip
	})

	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, item := range list {
		if _, ok := seen[item.ip]; !ok {
			seen[item.ip] = struct{}{}
			out = append(out, item.ip)
		}
	}
	return out
}

// GetPrimaryLANInfo returns filtered LAN IPv4 addresses, the primary IP, and the uppercase MAC
// address of the interface owning the primary IP (used for Wake-on-LAN).
func GetPrimaryLANInfo(filterVirtual bool) ([]string, string, string) {
	ips := GetFilteredLANIPs(filterVirtual)
	if len(ips) == 0 {
		return nil, "127.0.0.1", ""
	}
	primary := ips[0]
	var mac string
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				var ip net.IP
				switch v := a.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && ip.To4() != nil && ip.To4().String() == primary {
					if len(iface.HardwareAddr) > 0 {
						mac = strings.ToUpper(iface.HardwareAddr.String())
					}
					return ips, primary, mac
				}
			}
		}
	}
	return ips, primary, mac
}

// RunDiscoveryServer listens on UDP :discoveryPort and answers ZERODESK_DISCOVER broadcasts on LAN.
func RunDiscoveryServer(ctx context.Context, discoveryPort, httpPort int, filterVirtual bool, log *logger.RollingLogger) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: discoveryPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("udp discovery listen :%d failed: %w", discoveryPort, err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	hostname, _ := os.Hostname()
	buf := make([]byte, 1024)
	log.Infof("UDP LAN discovery listening on 0.0.0.0:%d (filter_virtual_nics=%v)", discoveryPort, filterVirtual)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}

		msg := strings.TrimSpace(string(buf[:n]))
		if msg != DiscoverMagic {
			continue
		}

		ips, primary, mac := GetPrimaryLANInfo(filterVirtual)
		reply := DiscoveryReply{
			Service:       "ZeroDesk",
			Version:       "1.0.0-stage3",
			Hostname:      hostname,
			IPs:           ips,
			PrimaryIP:     primary,
			MacAddress:    mac,
			HTTPPort:      httpPort,
			DiscoveryPort: discoveryPort,
		}
		payload, _ := json.Marshal(reply)
		_, _ = conn.WriteToUDP(payload, remoteAddr)
		log.Infof("Discovery probe from %s answered (primary=%s, mac=%s)", remoteAddr.String(), primary, mac)
	}
}
