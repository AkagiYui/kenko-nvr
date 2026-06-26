package api

import (
	"net/http"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// redactNotifications blanks write-only secrets before sending config to the UI.
func redactNotifications(c database.NotificationConfig) database.NotificationConfig {
	c.Email.Password = ""
	c.MQTT.Password = ""
	c.WebPush.PrivateKey = "" // never leaves the server
	return c
}

func (s *Server) handleGetNotifications(w http.ResponseWriter, r *http.Request) {
	c, err := s.db.Settings.Notifications()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, redactNotifications(c))
}

func (s *Server) handleSetNotifications(w http.ResponseWriter, r *http.Request) {
	var in database.NotificationConfig
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	existing, _ := s.db.Settings.Notifications()
	mergeNotificationSecrets(&in, existing)

	// Generate a VAPID keypair the first time Web Push is enabled.
	if in.WebPush.Enabled && (in.WebPush.PublicKey == "" || in.WebPush.PrivateKey == "") {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "generating VAPID keys: "+err.Error())
			return
		}
		in.WebPush.PrivateKey, in.WebPush.PublicKey = priv, pub
	}

	if err := s.db.Settings.SetNotifications(in); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, redactNotifications(in))
}

// mergeNotificationSecrets keeps stored secrets when the client submits blanks
// (it never receives them), mirroring the S3 settings behaviour.
func mergeNotificationSecrets(in *database.NotificationConfig, existing database.NotificationConfig) {
	if in.Email.Password == "" {
		in.Email.Password = existing.Email.Password
	}
	if in.MQTT.Password == "" {
		in.MQTT.Password = existing.MQTT.Password
	}
	// VAPID keys are managed server-side; always carry them forward.
	in.WebPush.PublicKey = existing.WebPush.PublicKey
	in.WebPush.PrivateKey = existing.WebPush.PrivateKey
}

func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var in database.NotificationConfig
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	existing, _ := s.db.Settings.Notifications()
	mergeNotificationSecrets(&in, existing)
	in.Enabled = true // a test should fire even if global delivery is off
	if err := s.notifier.TestChannels(r.Context(), in); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleVAPIDPublicKey returns the Web Push public key, generating and storing a
// keypair on first use so any authenticated browser can subscribe.
func (s *Server) handleVAPIDPublicKey(w http.ResponseWriter, r *http.Request) {
	c, err := s.db.Settings.Notifications()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c.WebPush.PublicKey == "" || c.WebPush.PrivateKey == "" {
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		c.WebPush.PrivateKey, c.WebPush.PublicKey = priv, pub
		if err := s.db.Settings.SetNotifications(c); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": c.WebPush.PublicKey})
}

// handleSubscribePush stores a browser's Web Push subscription.
func (s *Server) handleSubscribePush(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if strings.TrimSpace(in.Endpoint) == "" || in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeErr(w, http.StatusBadRequest, "incomplete subscription")
		return
	}
	if err := s.db.Push.Upsert(database.PushSubscription{
		ID:       uuid.NewString(),
		Endpoint: in.Endpoint,
		P256dh:   in.Keys.P256dh,
		Auth:     in.Keys.Auth,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
