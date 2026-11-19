package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPrintLifecycle(t *testing.T) {
	started := time.Unix(1770000000, 0)
	job := &printJob{ID: "job", Started: started, State: "PRINTING", Name: "part.gcode"}
	var state printState
	messages, upload := state.update(true, jobs{Current: job}, started)
	if len(messages) != 1 || messages[0].Current != started.Unix() ||
		!messages[0].Status.State.Flags.Printing || upload {
		t.Fatalf("first printing status: %+v, upload=%v", messages, upload)
	}
	_, upload = state.update(true, jobs{Current: job}, started)
	if !upload {
		t.Fatal("second printing status did not release snapshot upload")
	}
	messages, upload = state.update(false, jobs{}, started)
	if messages[0].Current != 0 || upload {
		t.Fatal("disconnect did not preserve the remote job without uploading")
	}
	job.Outcome = "finished"
	messages, upload = state.update(true, jobs{Last: job}, started)
	if len(messages) != 2 || messages[0].Event.Type != "PrintDone" ||
		messages[1].Current != -1 || upload {
		t.Fatalf("completion: %+v, upload=%v", messages, upload)
	}
}

func TestSnapshotUploadAndConditionalRead(t *testing.T) {
	started := time.Unix(1770000000, 0).UTC()
	modified := started.Add(time.Minute).Format(http.TimeFormat)
	picture := []byte{0xff, 0xd8, 0xff, 0xd9}
	prusa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pcl-token" {
			http.Error(w, "wrong authentication", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/cameras":
			if r.URL.Query().Get("device") != "printer" {
				t.Error("camera lookup lacks device filter")
			}
			json.NewEncoder(w).Encode(map[string][]camera{
				"items": {{Name: "buddy", Device: "printer", Snapshot: true}},
			})
		case "/api/v1/cameras/buddy/snapshot":
			if r.Header.Get("If-Modified-Since") == modified {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Last-Modified", modified)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(picture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer prusa.Close()

	uploads := make(chan struct{}, 2)
	obico := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/octo/pic/" ||
			r.Header.Get("Authorization") != "Token obico-token" {
			http.Error(w, "wrong upload request", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("pic")
		if err != nil {
			t.Errorf("multipart pic: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, picture) || header.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("unexpected multipart frame: %x, %v, %v", got, header.Header, err)
		}
		uploads <- struct{}{}
	}))
	defer obico.Close()

	b := bridge{
		config:  settings{PrusaURL: prusa.URL, PrusaToken: "pcl-token", ObicoURL: obico.URL},
		printer: mapping{DeviceID: "printer", Token: "obico-token"},
	}
	job := &printJob{ID: "job", Started: started}
	for range 2 {
		if err := b.snapshot(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(uploads); got != 1 {
		t.Fatalf("uploaded %d frames, want 1", got)
	}
}

func TestSnapshotCameraMustBeAssigned(t *testing.T) {
	items := []camera{
		{Name: "wrong", Device: "other", Snapshot: true},
		{Name: "one", Device: "printer", Snapshot: true},
		{Name: "two", Device: "printer", Snapshot: true},
	}
	if _, err := chooseCamera(items, "printer", ""); err == nil {
		t.Fatal("accepted ambiguous camera")
	}
	if got, err := chooseCamera(items, "printer", "two"); err != nil || got != "two" {
		t.Fatalf("explicit camera: %q, %v", got, err)
	}
	if _, err := chooseCamera(items, "printer", "wrong"); err == nil {
		t.Fatal("accepted camera assigned to another printer")
	}
}
