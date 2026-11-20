package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

func TestDashboardOpenAPIIsAcceptedByPrusa(t *testing.T) {
	if _, err := routesapi.Describe(wirerpc.Info{Name: name, Version: version, Contract: wirerpc.Contract}, dashboardAPI); err != nil {
		t.Fatal(err)
	}
}

type testAccess struct{}

func TestTimelapseURLsStayOnTheConfiguredObicoMediaOrigin(t *testing.T) {
	config := settings{ObicoURL: "http://127.0.0.1:3334", ObicoBrowserURL: "https://obico.example"}
	for _, example := range []struct{ url, want string }{
		{"/media/timelapse/video.mp4?sig=signed", "https://obico.example/media/timelapse/video.mp4?sig=signed"},
		{"https://obico.example/media/timelapse/video.mp4", "https://obico.example/media/timelapse/video.mp4"},
		{"https://other.example/media/video.mp4", ""},
		{"//other.example/media/video.mp4", ""},
		{"https://user:password@obico.example/media/video.mp4", ""},
		{"/media/%2e%2e/admin", ""},
		{"/accounts/login/", ""},
	} {
		if got := timelapseURL(config, example.url); got != example.want {
			t.Errorf("timelapseURL(%q) = %q, want %q", example.url, got, example.want)
		}
	}
}

func TestAcknowledgementChecksTheActivePrint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"current_print":{"id":20,"alerted_at":"2026-11-20T02:00:00Z"}}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/printers/47/acknowledge_alert/" {
			t.Error("unexpected Obico alert action")
			http.NotFound(w, r)
			return
		}
		calls++
	}))
	defer server.Close()
	config := settings{ObicoURL: server.URL, ObicoAccessToken: "access"}
	for _, id := range []int{19, 20} {
		changed, err := sendMonitoringAction(t.Context(), config, 47, monitoringAction{PrintID: id, Acknowledge: true})
		if err != nil || changed != (id == 20) {
			t.Fatalf("print %d: changed=%v, error=%v", id, changed, err)
		}
	}
	if calls != 1 {
		t.Fatalf("sent %d acknowledgements, want 1", calls)
	}
}

func (testAccess) Check(_ context.Context, params accessapi.CheckParams) (accessapi.Decision, error) {
	if params.Action == "configure_resource" {
		return accessapi.Decision{Allowed: params.Subject == "user:admin" &&
			params.ObjectType == "resource_kind" && params.ObjectID == "sink"}, nil
	}
	return accessapi.Decision{Allowed: params.Subject == "user:operator" &&
		params.Action == "operate" && params.ObjectType == "device" && params.ObjectID == "core-one"}, nil
}

func TestNotificationSettingsRequireAdminAndHideChannelCredentials(t *testing.T) {
	obico := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer obico-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == "PATCH" {
			var data map[string]any
			if json.NewDecoder(r.Body).Decode(&data) != nil || len(data) != 5 || data["name"] != "email" || data["enabled"] != true {
				t.Error("unexpected notification update")
			}
			return
		}
		channel := `{"id":7,"name":"email","enabled":true,"config":{"password":"private"}}`
		if r.URL.Path == notificationPath {
			channel = "[" + channel + "]"
		}
		_, _ = w.Write([]byte(channel))
	}))
	defer obico.Close()
	var dashboard dashboard
	dashboard.configure(settings{ObicoURL: obico.URL, ObicoAccessToken: "obico-access"}, nil)
	for _, example := range []struct {
		principal, method, path, body string
		status                        int
	}{
		{"user:operator", "GET", "/notifications", "", 404},
		{"user:admin", "GET", "/notifications", "", 200},
		{"user:admin", "POST", "/notifications/7", `{"enabled":true,"notify_on_failure_alert":true,"notify_on_print_done":false,"notify_on_print_start":false}`, 204},
	} {
		request, err := (routesapi.Request{Method: example.method, Path: example.path, Principal: example.principal, Body: []byte(example.body)}).HTTP(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		res := httptest.NewRecorder()
		dashboard.routes(testDevices{}, testAccess{}).ServeHTTP(res, request)
		if res.Code != example.status || strings.Contains(res.Body.String(), "private") {
			t.Fatalf("%s %s returned %d: %s", example.method, example.path, res.Code, res.Body.String())
		}
	}
}

func TestMonitoringMakesTheScopedObicoChangeOnlyForAnOperator(t *testing.T) {
	obico := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/octo/printer/" {
			_, _ = w.Write([]byte(`{"printer":{"id":47}}`))
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/printers/47/" ||
			r.Header.Get("Authorization") != "Bearer obico-access" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || !body["watching_enabled"] {
			t.Errorf("unexpected monitoring update: %v, %v", body, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer obico.Close()
	var dashboard dashboard
	dashboard.configure(settings{ObicoURL: obico.URL, ObicoAccessToken: "obico-access"}, []mapping{
		{DeviceID: "core-one", Token: "agent-token"},
	})
	handler := dashboard.routes(testDevices{}, testAccess{})
	for _, attempt := range []struct {
		principal string
		body      string
		want      int
	}{
		{"user:viewer", `{"watching":true}`, http.StatusNotFound},
		{"user:operator", `{"action_on_failure":"PAUSE"}`, http.StatusBadRequest},
		{"user:operator", `{"watching":true}`, http.StatusNoContent},
	} {
		request, err := (routesapi.Request{Method: "POST", Path: "/printers/core-one/monitoring",
			Principal: attempt.principal, Body: []byte(attempt.body)}).HTTP(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != attempt.want {
			t.Errorf("%s with %s: got %d, want %d", attempt.principal, attempt.body, response.Code, attempt.want)
		}
	}
}

type testDevices struct{}

func (testDevices) Get(_ context.Context, params deviceapi.GetParams) (deviceapi.Device, error) {
	if params.Principal != "user:test" || params.ID != "core-one" {
		return deviceapi.Device{}, errors.New("denied")
	}
	return deviceapi.Device{Name: "CORE One", Connected: true}, nil
}

func TestObicoDashboardKeepsOnlyThePairedPrintersData(t *testing.T) {
	obico := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := "Bearer account-access"
		if r.URL.Path == "/api/v1/octo/printer/" {
			expected = "Token agent-token"
		}
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/octo/printer/":
			_, _ = w.Write([]byte(`{"printer":{"id":47}}`))
		case "/api/v1/printers/47/":
			_, _ = w.Write([]byte(`{"id":47,"watching_enabled":true,"auth_token":"agent-token","current_print":{"id":20,"alerted_at":"2026-11-20T02:00:00Z"}}`))
		case "/api/v1/prints/":
			if r.URL.Query().Get("filter_by_printer_ids[]") != "47" {
				t.Error("Obico history request lacked the printer filter")
			}
			_, _ = w.Write([]byte(`[
				{"id":20,"filename":"in-progress.gcode","printer":{"id":47}},
				{"id":19,"filename":"owned.gcode","printer":{"id":47,"auth_token":"agent-token"}},
				{"id":18,"filename":"foreign.gcode","printer":{"id":48}}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer obico.Close()
	dashboard, err := readObicoDashboard(t.Context(), settings{
		ObicoURL: obico.URL, ObicoAccessToken: "account-access",
	}, mapping{Token: "agent-token"})
	if err != nil || !dashboard.Configured || !dashboard.Watching ||
		dashboard.Current == nil || dashboard.Current.AlertedAt == nil ||
		len(dashboard.Recent) != 1 || dashboard.Recent[0].Filename != "owned.gcode" {
		t.Fatalf("unexpected Obico data: %+v, %v", dashboard, err)
	}
}

func TestOverviewReportsMappingsAndConnectionWithoutTokens(t *testing.T) {
	var dashboard dashboard
	dashboard.configure(settings{}, []mapping{
		{DeviceID: "core-one", Camera: "bed", Token: "server-secret"},
		{DeviceID: "restricted", Camera: "private-camera", Token: "second-secret"},
	})
	dashboard.connected("core-one", true)
	response := httptest.NewRecorder()
	request, err := (routesapi.Request{Method: "GET", Path: "/overview", Principal: "user:test"}).HTTP(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dashboard.routes(testDevices{}, nil).ServeHTTP(response, request)
	if got := response.Body.String(); response.Code != 200 ||
		!strings.Contains(got, `"obico_connected":true`) ||
		!strings.Contains(got, `"camera":"bed"`) ||
		strings.Contains(got, "server-secret") || strings.Contains(got, "token") ||
		strings.Contains(got, "restricted") || strings.Contains(got, "private-camera") {
		t.Fatalf("unexpected overview: %s", got)
	}
}
