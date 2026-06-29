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
	// Copy the channel slice so blanking does not mutate the caller's array.
	chans := make([]database.NotificationChannel, len(c.Channels))
	copy(chans, c.Channels)
	for i := range chans {
		chans[i].Email.Password = ""
		chans[i].MQTT.Password = ""
	}
	c.Channels = chans
	c.WebPush.PrivateKey = "" // never leaves the server
	return c
}

// anyWebPushChannel reports whether any channel uses browser push.
func anyWebPushChannel(c database.NotificationConfig) bool {
	for _, ch := range c.Channels {
		if ch.Type == database.ChannelWebPush {
			return true
		}
	}
	return false
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

	// Generate the global VAPID keypair the first time a webpush channel exists.
	if anyWebPushChannel(in) && (in.WebPush.PublicKey == "" || in.WebPush.PrivateKey == "") {
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
// (it never receives them). Channel secrets are matched by channel ID; the VAPID
// keypair is global and always carried forward.
func mergeNotificationSecrets(in *database.NotificationConfig, existing database.NotificationConfig) {
	byID := make(map[string]database.NotificationChannel, len(existing.Channels))
	for _, ch := range existing.Channels {
		byID[ch.ID] = ch
	}
	for i := range in.Channels {
		ch := &in.Channels[i]
		old, ok := byID[ch.ID]
		if !ok {
			continue
		}
		if ch.Email.Password == "" {
			ch.Email.Password = old.Email.Password
		}
		if ch.MQTT.Password == "" {
			ch.MQTT.Password = old.MQTT.Password
		}
	}
	in.WebPush.PublicKey = existing.WebPush.PublicKey
	in.WebPush.PrivateKey = existing.WebPush.PrivateKey
}

// handleTestNotification fires a synthetic notification through one channel
// (identified by ?channelId=), or through every enabled channel if none is given.
func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var in database.NotificationConfig
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	existing, _ := s.db.Settings.Notifications()
	mergeNotificationSecrets(&in, existing)

	if id := r.URL.Query().Get("channelId"); id != "" {
		for _, ch := range in.Channels {
			if ch.ID == id {
				if err := s.notifier.TestChannel(r.Context(), in, ch); err != nil {
					writeErr(w, http.StatusBadGateway, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
				return
			}
		}
		writeErr(w, http.StatusBadRequest, "channel not found")
		return
	}

	var firstErr error
	tested := 0
	for _, ch := range in.Channels {
		if !ch.Enabled {
			continue
		}
		tested++
		if err := s.notifier.TestChannel(r.Context(), in, ch); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if tested == 0 {
		writeErr(w, http.StatusBadGateway, "没有启用任何通知渠道")
		return
	}
	if firstErr != nil {
		writeErr(w, http.StatusBadGateway, firstErr.Error())
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
