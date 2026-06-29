// Package hadiscovery publishes Home Assistant MQTT discovery messages so each
// camera appears in Home Assistant as a device with a motion binary_sensor and an
// online/connectivity binary_sensor. It reuses the notifications MQTT broker
// connection and is entirely pure-Go (MQTT publishes only), preserving the
// CGO-free build.
package hadiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
	"github.com/AkagiYui/kenko-nvr/internal/notify"
)

// CamState is a camera's live state, as reported by the StatusProvider for the
// periodic state resync.
type CamState struct {
	Online bool
	Motion bool
}

// StatusProvider supplies the current live state of every camera. The manager
// implements it; keeping it an interface avoids an import cycle.
type StatusProvider interface {
	HAStates() map[string]CamState
}

// Publisher publishes and refreshes Home Assistant discovery + state messages.
type Publisher struct {
	DB       *database.DB
	Notifier *notify.Notifier
	Status   StatusProvider
	Log      *slog.Logger

	mu        sync.Mutex
	warnedOff bool
}

// Run publishes discovery for all cameras and then periodically refreshes the
// configs and current state until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	p.resync()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.resync()
		}
	}
}

// resync (re)publishes discovery configs and the current state of every camera.
func (p *Publisher) resync() {
	ha, mq, ok := p.snapshot()
	if !ok {
		return
	}
	cams, err := p.DB.Cameras.List()
	if err != nil {
		return
	}
	var states map[string]CamState
	if p.Status != nil {
		states = p.Status.HAStates()
	}
	for _, cam := range cams {
		p.publishConfig(ha, mq, cam)
		st := states[cam.ID]
		p.publishState(ha, mq, cam.ID, availabilityPayload(st.Online), motionPayload(st.Motion))
	}
}

// Configure publishes the discovery config for one camera (called when a camera
// is added or changed).
func (p *Publisher) Configure(cam database.Camera) {
	ha, mq, ok := p.snapshot()
	if !ok {
		return
	}
	p.publishConfig(ha, mq, cam)
}

// Remove clears a camera's discovery config and marks it offline.
func (p *Publisher) Remove(cameraID string) {
	ha, mq, ok := p.snapshot()
	if !ok {
		return
	}
	p.clear(ha, mq, "binary_sensor", node(cameraID), "motion")
	p.clear(ha, mq, "binary_sensor", node(cameraID), "online")
	p.pub(mq, ha.BaseTopic+"/"+cameraID+"/availability", []byte("offline"), true)
}

// SetMotion publishes a camera's current motion state.
func (p *Publisher) SetMotion(cameraID string, on bool) {
	ha, mq, ok := p.snapshot()
	if !ok {
		return
	}
	p.pub(mq, ha.BaseTopic+"/"+cameraID+"/motion", []byte(motionPayload(on)), true)
}

// SetAvailability publishes a camera's current online state.
func (p *Publisher) SetAvailability(cameraID string, online bool) {
	ha, mq, ok := p.snapshot()
	if !ok {
		return
	}
	p.pub(mq, ha.BaseTopic+"/"+cameraID+"/availability", []byte(availabilityPayload(online)), true)
}

// --- internals ---------------------------------------------------------------

// snapshot reads the current HA + MQTT settings, returning ok=false (once warned)
// when discovery is disabled or the MQTT broker is unconfigured.
func (p *Publisher) snapshot() (database.HAConfig, database.MQTTConfig, bool) {
	ha, err := p.DB.Settings.HomeAssistant()
	if err != nil || !ha.Enabled {
		return ha, database.MQTTConfig{}, false
	}
	if ha.DiscoveryPrefix == "" {
		ha.DiscoveryPrefix = "homeassistant"
	}
	if ha.BaseTopic == "" {
		ha.BaseTopic = "kenko-nvr"
	}
	nc, err := p.DB.Settings.Notifications()
	mq, ok := nc.FirstMQTT()
	if err != nil || !ok {
		p.warnOnce("home assistant discovery enabled but notifications MQTT broker is not configured")
		return ha, database.MQTTConfig{}, false
	}
	return ha, mq, true
}

func (p *Publisher) warnOnce(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warnedOff {
		return
	}
	p.warnedOff = true
	if p.Log != nil {
		p.Log.Warn(msg)
	}
}

func (p *Publisher) publishConfig(ha database.HAConfig, mq database.MQTTConfig, cam database.Camera) {
	dev := haDevice(cam)
	avail := ha.BaseTopic + "/" + cam.ID + "/availability"

	motion := map[string]any{
		"name":                  "Motion",
		"unique_id":             "kenko_" + cam.ID + "_motion",
		"device_class":          "motion",
		"state_topic":           ha.BaseTopic + "/" + cam.ID + "/motion",
		"payload_on":            "ON",
		"payload_off":           "OFF",
		"availability_topic":    avail,
		"payload_available":     "online",
		"payload_not_available": "offline",
		"device":                dev,
	}
	online := map[string]any{
		"name":         "Online",
		"unique_id":    "kenko_" + cam.ID + "_online",
		"device_class": "connectivity",
		"state_topic":  avail,
		"payload_on":   "online",
		"payload_off":  "offline",
		"device":       dev,
	}
	p.pubJSON(mq, configTopic(ha.DiscoveryPrefix, "binary_sensor", node(cam.ID), "motion"), motion)
	p.pubJSON(mq, configTopic(ha.DiscoveryPrefix, "binary_sensor", node(cam.ID), "online"), online)
}

func (p *Publisher) publishState(ha database.HAConfig, mq database.MQTTConfig, cameraID, avail, motion string) {
	p.pub(mq, ha.BaseTopic+"/"+cameraID+"/availability", []byte(avail), true)
	p.pub(mq, ha.BaseTopic+"/"+cameraID+"/motion", []byte(motion), true)
}

func (p *Publisher) clear(ha database.HAConfig, mq database.MQTTConfig, component, node, object string) {
	p.pub(mq, configTopic(ha.DiscoveryPrefix, component, node, object), nil, true)
}

func (p *Publisher) pubJSON(mq database.MQTTConfig, topic string, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	p.pub(mq, topic, payload, true)
}

func (p *Publisher) pub(mq database.MQTTConfig, topic string, payload []byte, retain bool) {
	if err := p.Notifier.PublishRaw(mq, topic, payload, retain); err != nil && p.Log != nil {
		p.Log.Debug("ha discovery publish failed", "topic", topic, "err", err)
	}
}

func haDevice(cam database.Camera) map[string]any {
	return map[string]any{
		"identifiers":  []string{"kenko_" + cam.ID},
		"name":         cam.Name,
		"manufacturer": "kenko-nvr",
		"model":        string(cam.SourceType),
	}
}

func configTopic(prefix, component, node, object string) string {
	return fmt.Sprintf("%s/%s/%s/%s/config", prefix, component, node, object)
}

// node returns an MQTT-topic-safe node id for a camera.
func node(cameraID string) string { return "kenko_" + cameraID }

func motionPayload(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

func availabilityPayload(online bool) string {
	if online {
		return "online"
	}
	return "offline"
}
