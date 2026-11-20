package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

type obicoPrint struct {
	ID                  int        `json:"id"`
	Filename            string     `json:"filename"`
	StartedAt           time.Time  `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	AlertedAt           *time.Time `json:"alerted_at,omitempty"`
	AlertAcknowledgedAt *time.Time `json:"alert_acknowledged_at,omitempty"`
	AlertMutedAt        *time.Time `json:"alert_muted_at,omitempty"`
	VideoURL            string     `json:"video_url,omitempty"`
	TaggedVideoURL      string     `json:"tagged_video_url,omitempty"`
}

func timelapseURL(config settings, raw string) string {
	if raw == "" {
		return ""
	}
	base := config.ObicoBrowserURL
	if base == "" {
		base = config.ObicoURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/media/") ||
		strings.Contains(u.Path, "\\") || path.Clean(u.Path) != u.Path {
		return ""
	}
	if u.IsAbs() || u.Host != "" {
		if u.Scheme+"://"+u.Host != base {
			return ""
		}
		return u.String()
	}
	return base + u.RequestURI()
}

func sanitizeTimelapse(config settings, print *obicoPrint) {
	print.VideoURL = timelapseURL(config, print.VideoURL)
	print.TaggedVideoURL = timelapseURL(config, print.TaggedVideoURL)
}

type obicoDashboard struct {
	Configured bool         `json:"configured"`
	Watching   bool         `json:"watching"`
	CanControl bool         `json:"can_control"`
	Current    *obicoPrint  `json:"current,omitempty"`
	Recent     []obicoPrint `json:"recent,omitempty"`
}

type permissionReader interface {
	Check(context.Context, accessapi.CheckParams) (accessapi.Decision, error)
}

func mayOperate(r *http.Request, access permissionReader, device string) bool {
	principal := routesapi.PrincipalOf(r)
	if principal == "" {
		return false
	}
	decision, err := access.Check(r.Context(), accessapi.CheckParams{
		Subject: principal, Action: "operate", ObjectType: "device", ObjectID: device,
	})
	return err == nil && decision.Allowed
}

func obicoRead(ctx context.Context, base, path, auth string, data any) error {
	res, err := call(ctx, http.MethodGet, base+path, auth, "", "", nil)
	if err != nil {
		return err
	}
	return decode(res, data)
}

func pairedObicoID(ctx context.Context, config settings, printer mapping) (int, error) {
	var identification struct {
		Printer struct {
			ID int `json:"id"`
		} `json:"printer"`
	}
	if err := obicoRead(ctx, config.ObicoURL, "/api/v1/octo/printer/", "Token "+printer.Token, &identification); err != nil {
		return 0, err
	}
	id := identification.Printer.ID
	if id <= 0 {
		return 0, fmt.Errorf("Obico did not return an assigned printer")
	}
	return id, nil
}

func readObicoDashboard(ctx context.Context, config settings, printer mapping) (obicoDashboard, error) {
	if config.ObicoAccessToken == "" {
		return obicoDashboard{}, nil
	}
	id, err := pairedObicoID(ctx, config, printer)
	if err != nil {
		return obicoDashboard{}, err
	}
	var remote struct {
		ID       int         `json:"id"`
		Watching bool        `json:"watching_enabled"`
		Current  *obicoPrint `json:"current_print"`
	}
	auth := "Bearer " + config.ObicoAccessToken
	path := "/api/v1/printers/" + strconv.Itoa(id) + "/"
	if err := obicoRead(ctx, config.ObicoURL, path, auth, &remote); err != nil {
		return obicoDashboard{}, err
	}
	if remote.ID != id {
		return obicoDashboard{}, fmt.Errorf("Obico returned a different printer")
	}
	var prints []struct {
		obicoPrint
		Printer struct {
			ID int `json:"id"`
		} `json:"printer"`
	}
	query := url.Values{"filter_by_printer_ids[]": {strconv.Itoa(id)}, "limit": {"6"}, "sorting": {"date_desc"}}
	if err := obicoRead(ctx, config.ObicoURL, "/api/v1/prints/?"+query.Encode(), auth, &prints); err != nil {
		return obicoDashboard{}, err
	}
	result := obicoDashboard{Configured: true, Watching: remote.Watching, Current: remote.Current}
	if result.Current != nil {
		sanitizeTimelapse(config, result.Current)
	}
	for _, print := range prints {
		if print.Printer.ID == id && (remote.Current == nil || print.ID != remote.Current.ID) {
			sanitizeTimelapse(config, &print.obicoPrint)
			result.Recent = append(result.Recent, print.obicoPrint)
			if len(result.Recent) == 5 {
				break
			}
		}
	}
	return result, nil
}

func (d *dashboard) obicoOverview(devices deviceReader, access permissionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, id := routesapi.PrincipalOf(r), r.PathValue("device")
		d.mu.RLock()
		mapping, found := d.mappings[id]
		config := d.settings
		d.mu.RUnlock()
		if principal == "" || !found {
			http.NotFound(w, r)
			return
		}
		if _, err := devices.Get(r.Context(), deviceapi.GetParams{Principal: principal, ID: id}); err != nil {
			http.NotFound(w, r)
			return
		}
		data, err := readObicoDashboard(r.Context(), config, mapping)
		if err != nil {
			http.Error(w, "Obico data unavailable", http.StatusBadGateway)
			return
		}
		data.CanControl = data.Configured && mayOperate(r, access, id)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(data)
	}
}

type monitoringAction struct {
	Watching    *bool `json:"watching,omitempty"`
	PrintID     int   `json:"print_id,omitempty"`
	Muted       *bool `json:"muted,omitempty"`
	Acknowledge bool  `json:"acknowledge,omitempty"`
}

func (a monitoringAction) valid() bool {
	if a.Watching != nil {
		return a.PrintID == 0 && a.Muted == nil && !a.Acknowledge
	}
	return a.PrintID > 0 && ((a.Muted != nil) != a.Acknowledge)
}

func sendMonitoringAction(ctx context.Context, config settings, remoteID int, action monitoringAction) (bool, error) {
	auth := "Bearer " + config.ObicoAccessToken
	path := "/api/v1/printers/" + strconv.Itoa(remoteID) + "/"
	method := http.MethodPatch
	var body *bytes.Buffer
	if action.Watching != nil {
		body = bytes.NewBufferString(fmt.Sprintf(`{"watching_enabled":%t}`, *action.Watching))
	} else {
		var printer struct {
			Current *obicoPrint `json:"current_print"`
		}
		if err := obicoRead(ctx, config.ObicoURL, path, auth, &printer); err != nil {
			return false, err
		}
		if printer.Current == nil || printer.Current.ID != action.PrintID {
			return false, nil
		}
		if action.Acknowledge && (printer.Current.AlertedAt == nil ||
			(printer.Current.AlertAcknowledgedAt != nil && !printer.Current.AlertedAt.After(*printer.Current.AlertAcknowledgedAt))) {
			return false, nil
		}
		method = http.MethodPost
		path += "acknowledge_alert/"
		if action.Muted != nil {
			path = "/api/v1/printers/" + strconv.Itoa(remoteID) + "/mute_current_print/?mute_alert=" + strconv.FormatBool(*action.Muted)
		}
		body = bytes.NewBufferString("{}")
	}
	res, err := call(ctx, method, config.ObicoURL+path, auth, "application/json", "", body)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return false, fmt.Errorf("Obico did not confirm the update")
	}
	return true, nil
}

func (d *dashboard) updateMonitoring(access permissionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("device")
		d.mu.RLock()
		printer, found := d.mappings[id]
		config := d.settings
		d.mu.RUnlock()
		if !found || !mayOperate(r, access, id) {
			http.NotFound(w, r)
			return
		}
		if config.ObicoAccessToken == "" {
			http.Error(w, "Obico API is not configured", http.StatusConflict)
			return
		}
		var action monitoringAction
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&action) != nil || !action.valid() {
			http.Error(w, "Expected a monitoring setting", http.StatusBadRequest)
			return
		}
		remoteID, err := pairedObicoID(r.Context(), config, printer)
		var changed bool
		if err == nil {
			changed, err = sendMonitoringAction(r.Context(), config, remoteID, action)
		}
		if !changed {
			http.Error(w, "Obico print or alert changed", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "Obico monitoring could not be updated", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
