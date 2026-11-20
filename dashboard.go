package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
)

//go:embed openapi.json
var dashboardAPI []byte

type dashboardPrinter struct {
	DeviceID       string `json:"device_id"`
	Camera         string `json:"camera,omitempty"`
	ObicoConnected bool   `json:"obico_connected"`
}

type visiblePrinter struct {
	dashboardPrinter
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
}

type dashboard struct {
	mu       sync.RWMutex
	printers []dashboardPrinter
	settings settings
	mappings map[string]mapping
}

func (d *dashboard) configure(config settings, printers []mapping) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.printers = make([]dashboardPrinter, len(printers))
	d.settings = config
	d.mappings = make(map[string]mapping, len(printers))
	for i, printer := range printers {
		d.printers[i] = dashboardPrinter{DeviceID: printer.DeviceID, Camera: printer.Camera}
		d.mappings[printer.DeviceID] = printer
	}
}

func (d *dashboard) connected(id string, connected bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.printers {
		if d.printers[i].DeviceID == id {
			d.printers[i].ObicoConnected = connected
			return
		}
	}
}

type deviceReader interface {
	Get(context.Context, deviceapi.GetParams) (deviceapi.Device, error)
}

func (d *dashboard) routes(devices deviceReader, access permissionReader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /notifications", d.notifications(access))
	mux.HandleFunc("POST /notifications/{channel}", d.notifications(access))
	mux.HandleFunc("POST /notifications/{channel}/test", d.notifications(access))
	mux.HandleFunc("GET /notification-providers", d.notifications(access))
	mux.HandleFunc("POST /notification-providers", d.notifications(access))
	mux.HandleFunc("GET /printers/{device}/obico", d.obicoOverview(devices, access))
	mux.HandleFunc("POST /printers/{device}/monitoring", d.updateMonitoring(access))
	mux.HandleFunc("GET /overview", func(w http.ResponseWriter, r *http.Request) {
		principal := routesapi.PrincipalOf(r)
		if principal == "" {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		d.mu.RLock()
		printers := append([]dashboardPrinter{}, d.printers...)
		d.mu.RUnlock()
		visible := make([]visiblePrinter, 0, len(printers))
		for _, printer := range printers {
			device, err := devices.Get(r.Context(), deviceapi.GetParams{Principal: principal, ID: printer.DeviceID})
			if err == nil {
				visible = append(visible, visiblePrinter{dashboardPrinter: printer, Name: device.Name, Connected: device.Connected})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			Printers []visiblePrinter `json:"printers"`
		}{Printers: visible})
	})
	return mux
}
