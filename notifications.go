package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

const notificationPath = "/api/v1/notification_settings/"

type notificationFlags struct {
	Enabled bool `json:"enabled"`
	Failure bool `json:"notify_on_failure_alert"`
	Done    bool `json:"notify_on_print_done"`
	Start   bool `json:"notify_on_print_start"`
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
		var flags struct {
			Enabled *bool `json:"enabled"`
			Failure *bool `json:"notify_on_failure_alert"`
			Done    *bool `json:"notify_on_print_done"`
			Start   *bool `json:"notify_on_print_start"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err != nil || id <= 0 || decoder.Decode(&flags) != nil ||
			flags.Enabled == nil || flags.Failure == nil || flags.Done == nil || flags.Start == nil {
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
			notificationFlags
		}{Name: channel.Name, notificationFlags: notificationFlags{
			Enabled: *flags.Enabled, Failure: *flags.Failure, Done: *flags.Done, Start: *flags.Start,
		}})
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
