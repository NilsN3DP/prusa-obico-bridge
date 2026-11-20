package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type printJob struct {
	ID        string    `json:"id"`
	Name      string    `json:"display_name"`
	Path      string    `json:"path"`
	State     string    `json:"state"`
	Progress  float64   `json:"progress"`
	Remaining int64     `json:"time_remaining_ms"`
	Started   time.Time `json:"started"`
	Outcome   string    `json:"outcome"`
}

type jobs struct{ Current, Last *printJob }

type camera struct {
	Name     string `json:"name"`
	Device   string `json:"device_id"`
	Snapshot bool   `json:"snapshot"`
}

func chooseCamera(items []camera, device, wanted string) (string, error) {
	var found string
	for _, item := range items {
		if item.Device != device || !item.Snapshot || item.Name == "" || (wanted != "" && item.Name != wanted) {
			continue
		}
		if found != "" {
			return "", errors.New("multiple assigned snapshot cameras; set camera in the printer mapping")
		}
		found = item.Name
	}
	if found == "" {
		return "", errors.New("no assigned snapshot camera matches the printer mapping")
	}
	return found, nil
}

type message struct {
	Current int64         `json:"current_print_ts,omitempty"`
	Event   *printEvent   `json:"event,omitempty"`
	Status  printerStatus `json:"status"`
}

type printEvent struct {
	Type string `json:"event_type"`
}

type printerStatus struct {
	Timestamp int64 `json:"_ts"`
	State     struct {
		Text  string `json:"text"`
		Flags struct {
			Operational bool `json:"operational"`
			Printing    bool `json:"printing"`
			Paused      bool `json:"paused"`
			Ready       bool `json:"ready"`
			Cancelling  bool `json:"cancelling"`
			Pausing     bool `json:"pausing"`
			Resuming    bool `json:"resuming"`
			Finishing   bool `json:"finishing"`
		} `json:"flags"`
	} `json:"state"`
	Job struct {
		File map[string]string `json:"file"`
	} `json:"job"`
	Progress struct {
		Completion float64 `json:"completion"`
		Remaining  int64   `json:"printTimeLeft"`
	} `json:"progress"`
}

func report(connected bool, job *printJob, now time.Time, ending string) message {
	m := message{}
	m.Status.Timestamp = now.Unix()
	state := &m.Status.State
	state.Text = "Offline"
	m.Status.Job.File = map[string]string{}
	if !connected {
		return m
	}
	m.Current = -1
	state.Text = "Operational"
	state.Flags.Operational = true
	state.Flags.Ready = job == nil
	if job == nil {
		return m
	}
	m.Current = job.Started.Unix()
	if ending != "" {
		m.Event = &printEvent{Type: ending}
	} else {
		switch strings.ToUpper(job.State) {
		case "PRINTING":
			state.Text, state.Flags.Printing = "Printing", true
		case "PAUSED":
			state.Text, state.Flags.Paused = "Paused", true
		case "CANCELLING":
			state.Text, state.Flags.Cancelling = "Cancelling", true
		case "PAUSING":
			state.Text, state.Flags.Pausing = "Pausing", true
		case "RESUMING":
			state.Flags.Resuming = true
		case "FINISHING":
			state.Flags.Finishing = true
		}
	}
	file := job.Name
	if file == "" && job.Path != "" {
		file = path.Base(job.Path)
	}
	if file == "" {
		file = "Print"
	}
	m.Status.Job.File = map[string]string{"name": file, "display": file, "path": job.Path}
	m.Status.Progress.Completion = job.Progress
	m.Status.Progress.Remaining = job.Remaining / 1000
	return m
}

type printState struct {
	id       string
	printing bool
}

var endings = map[string]string{"finished": "PrintDone", "failed": "PrintFailed", "cancelled": "PrintCancelled"}

func (s *printState) update(connected bool, jobs jobs, now time.Time) ([]message, bool) {
	if !connected {
		s.printing = false
		return []message{report(false, nil, now, "")}, false
	}
	var messages []message
	var id string
	if jobs.Current != nil {
		id = jobs.Current.ID
	}
	if s.id != "" && s.id != id && jobs.Last != nil && jobs.Last.ID == s.id {
		if end := endings[jobs.Last.Outcome]; end != "" && jobs.Last.Started.Unix() > 0 {
			messages = append(messages, report(true, jobs.Last, now, end))
		}
	}
	current := report(true, jobs.Current, now, "")
	printing := current.Status.State.Flags.Printing
	upload := printing && s.printing && s.id == id
	s.id, s.printing = id, printing
	return append(messages, current), upload
}

const timeout = 8 * time.Second
const maxImage = 8 << 20

var client = &http.Client{
	Timeout:       timeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func call(ctx context.Context, method, address, auth, contentType, modified string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, errors.New("invalid endpoint")
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("If-Modified-Since", modified)
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("request failed")
	}
	if res.StatusCode != http.StatusNotModified && (res.StatusCode < 200 || res.StatusCode >= 300) {
		res.Body.Close()
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return res, nil
}

func decode(res *http.Response, dst any) error {
	defer res.Body.Close()
	if json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(dst) != nil {
		return errors.New("invalid JSON response")
	}
	return nil
}

func link(ctx context.Context, base, code string) error {
	res, err := call(ctx, "POST", base+"/api/v1/octo/verify/?code="+code, "", "", "", nil)
	if err != nil {
		return fmt.Errorf("Obico linking: %w", err)
	}
	var data struct {
		Printer struct {
			Token string `json:"auth_token"`
		}
	}
	if err := decode(res, &data); err != nil || !token.MatchString(data.Printer.Token) {
		return errors.New("Obico did not return a printer token")
	}
	fmt.Println(data.Printer.Token)
	return nil
}

type bridge struct {
	config     settings
	printer    mapping
	state      printState
	freshFrame struct{ job, camera, modified string }
}

func (b *bridge) prusa(ctx context.Context, p string, dst any) error {
	res, err := call(ctx, "GET", b.config.PrusaURL+p, "Bearer "+b.config.PrusaToken, "", "", nil)
	if err != nil {
		return err
	}
	return decode(res, dst)
}

func (b *bridge) snapshot(ctx context.Context, job *printJob) error {
	var cameras struct{ Items []camera }
	if err := b.prusa(ctx, "/api/v1/cameras?device="+url.QueryEscape(b.printer.DeviceID), &cameras); err != nil {
		return err
	}
	selected, err := chooseCamera(cameras.Items, b.printer.DeviceID, b.printer.Camera)
	if err != nil {
		return err
	}
	previous := b.freshFrame
	sameCameraJob := previous.job == job.ID && previous.camera == selected
	modified := ""
	if sameCameraJob {
		modified = previous.modified
	}
	res, err := call(ctx, "GET", b.config.PrusaURL+"/api/v1/cameras/"+url.PathEscape(selected)+"/snapshot",
		"Bearer "+b.config.PrusaToken, "", modified, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified {
		return nil
	}
	modified = res.Header.Get("Last-Modified")
	if taken, err := http.ParseTime(modified); err == nil && taken.Before(job.Started.Truncate(time.Second)) {
		return nil
	}
	contentType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if contentType != "image/jpeg" {
		return errors.New("camera did not return a JPEG")
	}
	jpeg, err := io.ReadAll(io.LimitReader(res.Body, maxImage+1))
	if err != nil || len(jpeg) < 4 || len(jpeg) > maxImage || jpeg[0] != 0xff || jpeg[1] != 0xd8 {
		return errors.New("invalid JPEG or frame exceeds 8 MiB")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="pic"; filename="snapshot.jpg"`},
		"Content-Type":        {"image/jpeg"},
	})
	if err != nil {
		return err
	}
	if _, err := part.Write(jpeg); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	upload, err := call(ctx, "POST", b.config.ObicoURL+"/api/v1/octo/pic/", "Token "+b.printer.Token,
		form.FormDataContentType(), "", &body)
	if err != nil {
		return fmt.Errorf("Obico snapshot upload: %w", err)
	}
	upload.Body.Close()
	if !sameCameraJob {
		log.Printf("[%s] forwarding snapshots for job %s", b.printer.DeviceID, job.ID)
	}
	b.freshFrame.job, b.freshFrame.camera, b.freshFrame.modified = job.ID, selected, modified
	return nil
}

func (b *bridge) poll(ctx context.Context, conn *websocket.Conn) (bool, error) {
	devicePath := "/api/v1/devices/" + url.PathEscape(b.printer.DeviceID)
	var device struct {
		ID        string `json:"id"`
		Connected *bool  `json:"connected"`
	}
	if err := b.prusa(ctx, devicePath, &device); err != nil {
		return false, fmt.Errorf("Connect Local device: %w", err)
	}
	if device.ID != b.printer.DeviceID || device.Connected == nil {
		return false, errors.New("unexpected Connect Local device response")
	}
	var queue jobs
	if *device.Connected {
		if err := b.prusa(ctx, devicePath+"/jobs", &queue); err != nil {
			return false, fmt.Errorf("Connect Local jobs: %w", err)
		}
		if job := queue.Current; job != nil && (job.ID == "" || job.Started.Unix() <= 0) {
			return false, errors.New("Connect Local returned a job without an ID or valid start")
		}
	}
	next := b.state
	messages, upload := next.update(*device.Connected, queue, time.Now())
	for _, msg := range messages {
		if conn.SetWriteDeadline(time.Now().Add(timeout)) != nil || conn.WriteJSON(msg) != nil {
			return true, errors.New("Obico WebSocket disconnected")
		}
	}
	b.state = next
	if upload {
		if err := b.snapshot(ctx, queue.Current); err != nil {
			return false, fmt.Errorf("snapshot: %w", err)
		}
	}
	return false, nil
}

func (b *bridge) session(ctx context.Context, connected func(bool)) error {
	address := "ws" + strings.TrimPrefix(b.config.ObicoURL, "http") + "/ws/dev/"
	dialer := websocket.Dialer{HandshakeTimeout: timeout, Proxy: http.ProxyFromEnvironment}
	conn, res, err := dialer.DialContext(ctx, address, http.Header{"Authorization": {"bearer " + b.printer.Token}})
	if err != nil {
		if res != nil {
			res.Body.Close()
			return fmt.Errorf("Obico WebSocket: HTTP %d", res.StatusCode)
		}
		return errors.New("Obico WebSocket connection failed")
	}
	defer conn.Close()
	connected(true)
	defer connected(false)
	b.state.printing = false
	conn.SetReadLimit(1 << 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	log.Printf("[%s] connected to Obico", b.printer.DeviceID)
	for {
		socketFailed, err := b.poll(ctx, conn)
		if socketFailed {
			return err
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("[%s] %v", b.printer.DeviceID, err)
		}
		timer := time.NewTimer(time.Duration(b.config.Interval) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-done:
			timer.Stop()
			return errors.New("Obico WebSocket disconnected")
		case <-timer.C:
		}
	}
}

func monitor(ctx context.Context, config settings, printer mapping, connected func(bool)) {
	bridge := bridge{config: config, printer: printer}
	for ctx.Err() == nil {
		if err := bridge.session(ctx, connected); err != nil && ctx.Err() == nil {
			log.Printf("[%s] %v", printer.DeviceID, err)
		}
		timer := time.NewTimer(time.Duration(config.Interval) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
