package onvif

import (
	"net"
	"strings"

	wsdiscovery "github.com/use-go/onvif/ws-discovery"
)

// Discovered is a camera found via WS-Discovery.
type Discovered struct {
	XAddr string `json:"xaddr"`
	Types string `json:"types"`
}

// Discover probes every usable network interface for ONVIF
// NetworkVideoTransmitter devices and returns their device-service addresses.
func Discover() ([]Discovered, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	var out []Discovered

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagMulticast == 0 ||
			iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		responses, err := wsdiscovery.SendProbe(
			iface.Name, nil,
			[]string{"dn:NetworkVideoTransmitter"},
			map[string]string{"dn": "http://www.onvif.org/ver10/network/wsdl"},
		)
		if err != nil {
			continue // interface can't multicast; try the next one
		}

		for _, resp := range responses {
			data := []byte(resp)
			xaddrs := findText(data, "XAddrs")
			if xaddrs == "" {
				continue
			}
			for _, addr := range strings.Fields(xaddrs) {
				if _, ok := seen[addr]; ok {
					continue
				}
				seen[addr] = struct{}{}
				out = append(out, Discovered{
					XAddr: addr,
					Types: findText(data, "Types"),
				})
			}
		}
	}
	return out, nil
}
