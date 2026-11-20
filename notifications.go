package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

const notificationPath = "/api/v1/notification_settings/"

type notificationProvider struct {
	name  string
	key   string
	value *regexp.Regexp
}

var notificationProviders = []notificationProvider{
	{name: "discord", key: "webhook_url", value: regexp.MustCompile(`^https://discord\.com/api(/v[0-9]+)?/webhooks/[0-9]+/[A-Za-z0-9_-]+/?$`)},
	{name: "email"},
	{name: "telegram", key: "chat_id", value: regexp.MustCompile(`^-?[0-9]{1,20}$`)},
	{name: "pushover", key: "user_key", value: regexp.MustCompile(`^[A-Za-z0-9]{30}$`)},
}

func availableNotificationProviders(r *http.Request, config settings, auth string) ([]notificationProvider, error) {
	var inventory struct {
		Plugins map[string]struct {
			Environment map[string]struct {
				Required bool    `json:"is_required"`
				Set      bool    `json:"is_set"`
				Value    *string `json:"value"`
			} `json:"env_vars"`
		} `json:"plugins"`
	}
	if err := obicoRead(r.Context(), config.ObicoURL, notificationPath+"available_plugins/", auth, &inventory); err != nil {
		return nil, err
	}
	available := make([]notificationProvider, 0, len(notificationProviders))
	for _, provider := range notificationProviders {
		plugin, found := inventory.Plugins[provider.name]
		if !found {
			continue
		}
		ready := true
		for _, variable := range plugin.Environment {
			if variable.Required && (!variable.Set || (variable.Value != nil && strings.TrimSpace(*variable.Value) == "")) {
				ready = false
				break
			}
		}
		if ready {
			available = append(available, provider)
		}
	}
	return available, nil
}

func configureNotificationProvider(w http.ResponseWriter, r *http.Request, config settings, auth string) {
	providers, err := availableNotificationProviders(r, config, auth)
	if err != nil {
		http.Error(w, "Obico notification providers unavailable", http.StatusBadGateway)
		return
	}
	if r.Method == http.MethodGet {
		names := make([]string, 0, len(providers))
		for _, provider := range providers {
			names = append(names, provider.name)
		}
		_ = json.NewEncoder(w).Encode(names)
		return
	}
	var input struct {
		Name       string `json:"name"`
		Credential string `json:"credential"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		http.Error(w, "Invalid notification channel", http.StatusBadRequest)
		return
	}
	for _, provider := range providers {
		if provider.name != input.Name {
			continue
		}
		credential := strings.TrimSpace(input.Credential)
		values := map[string]string{}
		if provider.value != nil {
			if !provider.value.MatchString(credential) {
				http.Error(w, "Invalid provider credential", http.StatusBadRequest)
				return
			}
			values[provider.key] = credential
		} else if credential != "" {
			http.Error(w, "Unexpected provider credential", http.StatusBadRequest)
			return
		}
		payload, _ := json.Marshal(map[string]any{
			"name": provider.name, "config": values, "enabled": false, "notify_on_failure_alert": true,
		})
		res, err := call(r.Context(), http.MethodPost, config.ObicoURL+notificationPath, auth,
			"application/json", "", bytes.NewReader(payload))
		if err != nil {
			http.Error(w, "Obico could not create the channel", http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			http.Error(w, "Obico did not confirm channel creation", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "Notification provider unavailable", http.StatusBadRequest)
}

type notificationFlags struct {
	Enabled   bool `json:"enabled"`
	Failure   bool `json:"notify_on_failure_alert"`
	Done      bool `json:"notify_on_print_done"`
	Start     bool `json:"notify_on_print_start"`
	Cancelled bool `json:"notify_on_print_cancelled"`
	Filament  bool `json:"notify_on_filament_change"`
	Heater    bool `json:"notify_on_heater_status"`
	Paused    bool `json:"notify_on_print_pause"`
	Resumed   bool `json:"notify_on_print_resume"`
}

type notificationUpdate struct {
	Enabled   *bool `json:"enabled,omitempty"`
	Failure   *bool `json:"notify_on_failure_alert,omitempty"`
	Done      *bool `json:"notify_on_print_done,omitempty"`
	Start     *bool `json:"notify_on_print_start,omitempty"`
	Cancelled *bool `json:"notify_on_print_cancelled,omitempty"`
	Filament  *bool `json:"notify_on_filament_change,omitempty"`
	Heater    *bool `json:"notify_on_heater_status,omitempty"`
	Paused    *bool `json:"notify_on_print_pause,omitempty"`
	Resumed   *bool `json:"notify_on_print_resume,omitempty"`
}

type notificationChannel struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	notificationFlags
}

func (d *dashboard) notifications(access permissionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := routesapi.PrincipalOf(r)
		if principal == "" {
			http.NotFound(w, r)
			return
		}
		decision, err := access.Check(r.Context(), accessapi.CheckParams{
			Subject: principal, Action: "configure_resource", ObjectType: "resource_kind", ObjectID: "sink",
		})
		if err != nil || !decision.Allowed {
			http.NotFound(w, r)
			return
		}
		d.mu.RLock()
		config := d.settings
		d.mu.RUnlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if config.ObicoAccessToken == "" {
			http.Error(w, "Obico API is not configured", http.StatusConflict)
			return
		}
		auth := "Bearer " + config.ObicoAccessToken
		if r.URL.Path == "/notification-providers" {
			configureNotificationProvider(w, r, config, auth)
			return
		}
		if r.Method == http.MethodGet {
			channels := []notificationChannel{}
			if obicoRead(r.Context(), config.ObicoURL, notificationPath, auth, &channels) != nil {
				http.Error(w, "Obico notification channels unavailable", http.StatusBadGateway)
				return
			}
			_ = json.NewEncoder(w).Encode(channels)
			return
		}
		id, err := strconv.Atoi(r.PathValue("channel"))
		var flags notificationUpdate
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err != nil || id <= 0 || decoder.Decode(&flags) != nil || flags == (notificationUpdate{}) {
			http.Error(w, "Invalid notification settings", http.StatusBadRequest)
			return
		}
		path := notificationPath + strconv.Itoa(id) + "/"
		var channel notificationChannel
		if obicoRead(r.Context(), config.ObicoURL, path, auth, &channel) != nil || channel.ID != id || channel.Name == "" {
			http.NotFound(w, r)
			return
		}
		payload, _ := json.Marshal(struct {
			Name string `json:"name"`
			notificationUpdate
		}{Name: channel.Name, notificationUpdate: flags})
		res, err := call(r.Context(), http.MethodPatch, config.ObicoURL+path, auth, "application/json", "", bytes.NewReader(payload))
		if err != nil {
			http.Error(w, "Obico could not save notification settings", http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			http.Error(w, "Obico did not confirm notification changes", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
