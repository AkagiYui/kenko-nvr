package onvif

import (
	"github.com/use-go/onvif/ptz"
	"github.com/use-go/onvif/xsd"
	xonvif "github.com/use-go/onvif/xsd/onvif"
)

// ContinuousMove starts a continuous PTZ movement. pan/tilt/zoom are velocities
// in [-1, 1]. The camera keeps moving until Stop is called (a safety timeout is
// also sent).
func (d *Device) ContinuousMove(profileToken string, pan, tilt, zoom float64) error {
	_, err := d.call(ptz.ContinuousMove{
		ProfileToken: xonvif.ReferenceToken(profileToken),
		Velocity: xonvif.PTZSpeed{
			PanTilt: xonvif.Vector2D{X: pan, Y: tilt},
			Zoom:    xonvif.Vector1D{X: zoom},
		},
		Timeout: xsd.Duration("PT5S"),
	})
	return err
}

// Stop halts any PTZ movement (both pan/tilt and zoom).
func (d *Device) Stop(profileToken string) error {
	_, err := d.call(ptz.Stop{
		ProfileToken: xonvif.ReferenceToken(profileToken),
		PanTilt:      xsd.Boolean(true),
		Zoom:         xsd.Boolean(true),
	})
	return err
}

// AbsoluteMove moves to an absolute pan/tilt/zoom position.
func (d *Device) AbsoluteMove(profileToken string, pan, tilt, zoom float64) error {
	_, err := d.call(ptz.AbsoluteMove{
		ProfileToken: xonvif.ReferenceToken(profileToken),
		Position: xonvif.PTZVector{
			PanTilt: xonvif.Vector2D{X: pan, Y: tilt},
			Zoom:    xonvif.Vector1D{X: zoom},
		},
	})
	return err
}

// GetPresets lists stored PTZ presets for the profile.
func (d *Device) GetPresets(profileToken string) ([]Preset, error) {
	body, err := d.call(ptz.GetPresets{
		ProfileToken: xonvif.ReferenceToken(profileToken),
	})
	if err != nil {
		return nil, err
	}
	return tokenizedElements(body, "Preset", "Name"), nil
}

// GotoPreset moves to a stored preset.
func (d *Device) GotoPreset(profileToken, presetToken string) error {
	_, err := d.call(ptz.GotoPreset{
		ProfileToken: xonvif.ReferenceToken(profileToken),
		PresetToken:  xonvif.ReferenceToken(presetToken),
	})
	return err
}
