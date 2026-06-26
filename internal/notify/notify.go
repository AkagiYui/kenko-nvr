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

// Notify delivers n to all enabled channels, honouring the per-camera+kind
// throttle. It returns quickly; delivery happens synchronously per channel but
// each channel failure is logged and isolated. Call from a goroutine if the
// caller must not block.
func (n *Notifier) Notify(ctx context.Context, msg Notification) {
	cfg := n.config()
	if !cfg.Enabled {
		return
	}
	if msg.Time.IsZero() {
		msg.Time = time.Now()
	}
	if !n.allow(msg, cfg.MinIntervalSeconds) {
		return
	}

	if cfg.Email.Enabled {
		if err := sendEmail(cfg.Email, msg); err != nil {
			n.logErr("email", err)
		}
	}
	if cfg.Webhook.Enabled {
		if err := sendWebhook(ctx, cfg.Webhook, msg); err != nil {
			n.logErr("webhook", err)
		}
	}
	if cfg.MQTT.Enabled {
		if err := n.publishMQTT(cfg.MQTT, msg); err != nil {
			n.logErr("mqtt", err)
		}
	}
	if cfg.WebPush.Enabled && n.Push != nil {
		n.sendWebPush(cfg.WebPush, msg)
	}
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

// TestChannels delivers a synthetic notification using the supplied config,
// returning the first error per enabled channel. Used by the settings "test"
// button so the user gets immediate feedback.
func (n *Notifier) TestChannels(ctx context.Context, cfg database.NotificationConfig) error {
	msg := Notification{
		Kind:       "test",
		CameraName: "测试",
		Title:      "Kenko NVR 通知测试",
		Body:       "如果你收到这条消息，说明通知渠道配置正确。",
		Time:       time.Now(),
	}
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if cfg.Email.Enabled {
		record(sendEmail(cfg.Email, msg))
	}
	if cfg.Webhook.Enabled {
		record(sendWebhook(ctx, cfg.Webhook, msg))
	}
	if cfg.MQTT.Enabled {
		record(n.publishMQTT(cfg.MQTT, msg))
	}
	if cfg.WebPush.Enabled && n.Push != nil {
		if err := n.sendWebPushErr(cfg.WebPush, msg); err != nil {
			record(err)
		}
	}
	if firstErr == nil && !anyEnabled(cfg) {
		return fmt.Errorf("没有启用任何通知渠道")
	}
	return firstErr
}

func anyEnabled(cfg database.NotificationConfig) bool {
	return cfg.Email.Enabled || cfg.Webhook.Enabled || cfg.MQTT.Enabled || cfg.WebPush.Enabled
}
