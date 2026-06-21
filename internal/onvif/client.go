// Package onvif wraps github.com/use-go/onvif to provide camera discovery,
// stream-URI resolution and PTZ control with a small, typed surface.
package onvif

import (
	"fmt"
	"io"
	"net/http"
	"time"

	goonvif "github.com/use-go/onvif"
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

// Connect opens an ONVIF device at xaddr (e.g. "192.168.1.10:80" or a full
// device-service URL) using the given credentials.
func Connect(xaddr, username, password string) (*Device, error) {
	dev, err := goonvif.NewDevice(goonvif.DeviceParams{
		Xaddr:      xaddr,
		Username:   username,
		Password:   password,
		HttpClient: &http.Client{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to onvif device: %w", err)
	}
	return &Device{dev: dev}, nil
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
		return body, fmt.Errorf("onvif returned %d", resp.StatusCode)
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

// GetInfo returns the device's manufacturer/model information.
func (d *Device) GetInfo() Info {
	di := d.dev.GetDeviceInfo()
	return Info{
		Manufacturer: di.Manufacturer,
		Model:        di.Model,
		Firmware:     di.FirmwareVersion,
		Serial:       di.SerialNumber,
	}
}
