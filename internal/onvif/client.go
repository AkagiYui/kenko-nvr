// Package onvif wraps github.com/use-go/onvif to provide camera discovery,
// stream-URI resolution and PTZ control with a small, typed surface.
package onvif

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	goonvif "github.com/use-go/onvif"
	"github.com/use-go/onvif/device"
	"github.com/use-go/onvif/media"
	xonvif "github.com/use-go/onvif/xsd/onvif"
)

// Device is a connected ONVIF camera.
type Device struct {
	dev *goonvif.Device
}

// Profile is a media profile exposed by the camera.
type Profile = tokenItem

// Preset is a PTZ preset.
type Preset = tokenItem

// Connect opens an ONVIF device at xaddr using the given credentials. The
// xaddr may be given as a bare host ("192.168.1.10"), a host:port, or a full
// device-service URL as returned by discovery
// ("http://192.168.1.10/onvif/device_service") — all are normalised to the
// host[:port] form the underlying library expects.
func Connect(xaddr, username, password string) (*Device, error) {
	dev, err := goonvif.NewDevice(goonvif.DeviceParams{
		Xaddr:      NormalizeXAddr(xaddr),
		Username:   username,
		Password:   password,
		HttpClient: &http.Client{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to onvif device: %w", err)
	}
	return &Device{dev: dev}, nil
}

// NormalizeXAddr reduces any accepted address form to host[:port], which is
// what use-go/onvif expects (it builds "http://<xaddr>/onvif/device_service").
func NormalizeXAddr(xaddr string) string {
	s := strings.TrimSpace(xaddr)
	if s == "" {
		return s
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Host
		}
	}
	// strip any path component, e.g. "192.168.5.19/onvif/device_service"
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

func (d *Device) call(method interface{}) ([]byte, error) {
	resp, err := d.dev.CallMethod(method)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("onvif returned %d: %s", resp.StatusCode, faultSummary(body))
	}
	return body, nil
}

// GetProfiles lists the camera's media profiles.
func (d *Device) GetProfiles() ([]Profile, error) {
	body, err := d.call(media.GetProfiles{})
	if err != nil {
		return nil, err
	}
	return tokenizedElements(body, "Profiles", "Name"), nil
}

// GetStreamURI resolves the RTSP URL for a media profile.
func (d *Device) GetStreamURI(profileToken string) (string, error) {
	body, err := d.call(media.GetStreamUri{
		StreamSetup: xonvif.StreamSetup{
			Stream: xonvif.StreamType("RTP-Unicast"),
			Transport: xonvif.Transport{
				Protocol: xonvif.TransportProtocol("RTSP"),
			},
		},
		ProfileToken: xonvif.ReferenceToken(profileToken),
	})
	if err != nil {
		return "", err
	}
	uri := findText(body, "Uri")
	if uri == "" {
		return "", fmt.Errorf("no stream URI in response")
	}
	return uri, nil
}

// Info holds basic device information.
type Info struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Firmware     string `json:"firmware"`
	Serial       string `json:"serial"`
}

// GetInfo returns the device's manufacturer/model information. It issues a
// GetDeviceInformation request and parses the response itself: the use-go/onvif
// library never populates its cached device info, so dev.GetDeviceInfo() always
// returns a zero-value struct — we have to ask the device directly.
func (d *Device) GetInfo() (Info, error) {
	body, err := d.call(device.GetDeviceInformation{})
	if err != nil {
		return Info{}, err
	}
	return parseDeviceInfo(body), nil
}

// parseDeviceInfo extracts the device fields from a GetDeviceInformationResponse.
func parseDeviceInfo(body []byte) Info {
	return Info{
		Manufacturer: findText(body, "Manufacturer"),
		Model:        findText(body, "Model"),
		Firmware:     findText(body, "FirmwareVersion"),
		Serial:       findText(body, "SerialNumber"),
	}
}
