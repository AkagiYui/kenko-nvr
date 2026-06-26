package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// --- Email (SMTP) -------------------------------------------------------------

func sendEmail(cfg database.EmailConfig, msg Notification) error {
	if cfg.Host == "" {
		return fmt.Errorf("smtp host not set")
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	recipients := splitList(cfg.To)
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients configured")
	}

	body := buildEmail(from, recipients, msg)
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", port))

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	// Port 465 uses implicit TLS; other ports use STARTTLS when UseTLS is set.
	if port == 465 {
		return sendSMTPImplicitTLS(addr, cfg.Host, auth, from, recipients, body)
	}
	if cfg.UseTLS {
		return sendSMTPStartTLS(addr, cfg.Host, auth, from, recipients, body)
	}
	return smtp.SendMail(addr, auth, from, recipients, body)
}

func sendSMTPImplicitTLS(addr, host string, auth smtp.Auth, from string, to []string, body []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	return smtpDeliver(c, auth, from, to, body)
}

func sendSMTPStartTLS(addr, host string, auth smtp.Auth, from string, to []string, body []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	return smtpDeliver(c, auth, from, to, body)
}

func smtpDeliver(c *smtp.Client, auth smtp.Auth, from string, to []string, body []byte) error {
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func buildEmail(from string, to []string, msg Notification) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Title)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "%s\r\n\r\n", msg.Body)
	fmt.Fprintf(&b, "摄像头：%s\r\n时间：%s\r\n", msg.CameraName, msg.Time.Format("2006-01-02 15:04:05"))
	return b.Bytes()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// --- Webhook ------------------------------------------------------------------

var webhookClient = &http.Client{Timeout: 10 * time.Second}

func sendWebhook(ctx context.Context, cfg database.WebhookConfig, msg Notification) error {
	if cfg.URL == "" {
		return fmt.Errorf("webhook url not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(msg.payload()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// --- MQTT ---------------------------------------------------------------------

type mqttConn struct {
	client mqtt.Client
	sig    string // settings signature this client was built for
}

func (c *mqttConn) close() {
	if c != nil && c.client != nil {
		c.client.Disconnect(250)
	}
}

func mqttSignature(cfg database.MQTTConfig) string {
	return strings.Join([]string{cfg.BrokerURL, cfg.Username, cfg.Password, cfg.ClientID}, "|")
}

func (n *Notifier) publishMQTT(cfg database.MQTTConfig, msg Notification) error {
	if cfg.BrokerURL == "" {
		return fmt.Errorf("mqtt broker not set")
	}
	topic := cfg.Topic
	if topic == "" {
		topic = "kenko-nvr/events"
	}
	client, err := n.mqttClient(cfg)
	if err != nil {
		return err
	}
	tok := client.Publish(topic, 0, false, msg.payload())
	if !tok.WaitTimeout(8 * time.Second) {
		return fmt.Errorf("mqtt publish timed out")
	}
	return tok.Error()
}

// mqttClient returns a connected client for cfg, rebuilding it if the settings
// changed since last time.
func (n *Notifier) mqttClient(cfg database.MQTTConfig) (mqtt.Client, error) {
	sig := mqttSignature(cfg)
	n.mu.Lock()
	if n.mqtt != nil && n.mqtt.sig == sig && n.mqtt.client.IsConnected() {
		c := n.mqtt.client
		n.mu.Unlock()
		return c, nil
	}
	old := n.mqtt
	n.mqtt = nil
	n.mu.Unlock()
	if old != nil {
		old.close()
	}

	clientID := cfg.ClientID
	if clientID == "" {
		clientID = "kenko-nvr"
	}
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.BrokerURL).
		SetClientID(clientID).
		SetConnectTimeout(8 * time.Second).
		SetAutoReconnect(true)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username).SetPassword(cfg.Password)
	}
	client := mqtt.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(8 * time.Second) {
		return nil, fmt.Errorf("mqtt connect timed out")
	}
	if err := tok.Error(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	n.mqtt = &mqttConn{client: client, sig: sig}
	n.mu.Unlock()
	return client, nil
}

// --- Web Push -----------------------------------------------------------------

func (n *Notifier) sendWebPush(cfg database.WebPushConfig, msg Notification) {
	_ = n.sendWebPushErr(cfg, msg)
}

func (n *Notifier) sendWebPushErr(cfg database.WebPushConfig, msg Notification) error {
	if cfg.PublicKey == "" || cfg.PrivateKey == "" {
		return fmt.Errorf("web push VAPID keys not generated")
	}
	subs, err := n.Push.List()
	if err != nil {
		return err
	}
	payload := webPushPayload(msg)
	var firstErr error
	for _, s := range subs {
		sub := &webpush.Subscription{
			Endpoint: s.Endpoint,
			Keys:     webpush.Keys{P256dh: s.P256dh, Auth: s.Auth},
		}
		resp, err := webpush.SendNotification(payload, sub, &webpush.Options{
			Subscriber:      subjectOrDefault(cfg.Subject),
			VAPIDPublicKey:  cfg.PublicKey,
			VAPIDPrivateKey: cfg.PrivateKey,
			TTL:             60,
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Prune expired/invalid subscriptions.
		if resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound {
			_ = n.Push.DeleteByEndpoint(s.Endpoint)
		}
		resp.Body.Close()
	}
	return firstErr
}

func webPushPayload(msg Notification) []byte {
	body, _ := json.Marshal(map[string]any{
		"title":    msg.Title,
		"body":     msg.Body,
		"camera":   msg.CameraName,
		"cameraId": msg.CameraID,
		"kind":     msg.Kind,
		"time":     msg.Time.UnixMilli(),
	})
	return body
}

func subjectOrDefault(s string) string {
	if s == "" {
		return "mailto:admin@example.com"
	}
	return s
}
