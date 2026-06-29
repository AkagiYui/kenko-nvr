// Package notify delivers alerts (motion, camera offline) to configured
// channels: SMTP email, HTTP webhook, MQTT and browser Web Push. All transports
// are pure-Go so the CGO-free build is preserved.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// Notification is a single alert to deliver.
type Notification struct {
	Kind       string    `json:"kind"` // "motion" | "offline"
	CameraID   string    `json:"cameraId"`
	CameraName string    `json:"camera"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Time       time.Time `json:"time"`
}

// PushLister enumerates stored Web Push subscriptions and can prune dead ones.
type PushLister interface {
	List() ([]database.PushSubscription, error)
	DeleteByEndpoint(endpoint string) error
}

// Notifier dispatches notifications to all enabled channels. It is safe for
// concurrent use.
type Notifier struct {
	ConfigFn func() database.NotificationConfig
	Push     PushLister
	Log      *slog.Logger

	mu       sync.Mutex
	lastSent map[string]time.Time // cameraID+kind -> last send
	mqtt     *mqttConn            // cached MQTT connection (lazy)
}

// Notify delivers msg to every enabled channel that handles its kind, honouring
// the per-camera+kind throttle. It returns quickly; delivery happens
// synchronously per channel but each channel failure is logged and isolated.
// Call from a goroutine if the caller must not block.
func (n *Notifier) Notify(ctx context.Context, msg Notification) {
	cfg := n.config()
	if !cfg.Enabled {
		return
	}
	if msg.Time.IsZero() {
		msg.Time = time.Now()
	}

	// Resolve the channels that want this kind before consuming the throttle, so
	// a suppressed kind does not eat the next allowed notification's window.
	var targets []database.NotificationChannel
	for _, ch := range cfg.Channels {
		if ch.Enabled && ch.WantsKind(msg.Kind, cfg) {
			targets = append(targets, ch)
		}
	}
	if len(targets) == 0 {
		return
	}
	if !n.allow(msg, cfg.MinIntervalSeconds) {
		return
	}

	for _, ch := range targets {
		if err := n.deliver(ctx, cfg, ch, msg); err != nil {
			n.logErr(channelLabel(ch), err)
		}
	}
}

// deliver dispatches msg through one channel according to its type.
func (n *Notifier) deliver(ctx context.Context, cfg database.NotificationConfig, ch database.NotificationChannel, msg Notification) error {
	switch ch.Type {
	case database.ChannelEmail:
		return sendEmail(ch.Email, msg)
	case database.ChannelWebhook:
		return sendWebhook(ctx, ch.Webhook, msg)
	case database.ChannelMQTT:
		return n.publishMQTT(ch.MQTT, msg)
	case database.ChannelWebPush:
		if n.Push == nil {
			return fmt.Errorf("web push not available")
		}
		wp := cfg.WebPush
		if ch.Subject != "" {
			wp.Subject = ch.Subject
		}
		return n.sendWebPushErr(wp, msg)
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

func channelLabel(ch database.NotificationChannel) string {
	if ch.Name != "" {
		return string(ch.Type) + ":" + ch.Name
	}
	return string(ch.Type)
}

func (n *Notifier) config() database.NotificationConfig {
	if n.ConfigFn == nil {
		return database.NotificationConfig{}
	}
	return n.ConfigFn()
}

// allow enforces the per-(camera,kind) minimum interval.
func (n *Notifier) allow(msg Notification, minIntervalSec int) bool {
	if minIntervalSec <= 0 {
		return true
	}
	key := msg.CameraID + "|" + msg.Kind
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.lastSent == nil {
		n.lastSent = make(map[string]time.Time)
	}
	last, ok := n.lastSent[key]
	if ok && time.Since(last) < time.Duration(minIntervalSec)*time.Second {
		return false
	}
	n.lastSent[key] = time.Now()
	return true
}

func (n *Notifier) logErr(channel string, err error) {
	if n.Log != nil {
		n.Log.Warn("notification delivery failed", "channel", channel, "err", err)
	}
}

// payload is the JSON shape used by webhook and Web Push bodies.
func (msg Notification) payload() []byte {
	b, _ := json.Marshal(msg)
	return b
}

// Close releases any cached connections (MQTT).
func (n *Notifier) Close() {
	n.mu.Lock()
	c := n.mqtt
	n.mqtt = nil
	n.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// testMessage is the synthetic notification used by the settings "test" button.
func testMessage() Notification {
	return Notification{
		Kind:       "test",
		CameraName: "测试",
		Title:      "Kenko NVR 通知测试",
		Body:       "如果你收到这条消息，说明通知渠道配置正确。",
		Time:       time.Now(),
	}
}

// TestChannel delivers a synthetic notification through one channel (regardless
// of its enabled flag), so the user gets immediate feedback while configuring it.
func (n *Notifier) TestChannel(ctx context.Context, cfg database.NotificationConfig, ch database.NotificationChannel) error {
	return n.deliver(ctx, cfg, ch, testMessage())
}
