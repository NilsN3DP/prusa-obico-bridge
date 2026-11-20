// Obico Bridge is built on the Prusa Connect SDK by Prusa Research a.s.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/prusa3d/prusa-connect/sdk/accessapi"
	"github.com/prusa3d/prusa-connect/sdk/deviceapi"
	"github.com/prusa3d/prusa-connect/sdk/routesapi"
	"github.com/prusa3d/prusa-connect/sdk/wire"
	"github.com/prusa3d/prusa-connect/sdk/wirerpc"
)

const name, version = "obico-bridge", "0.1.4"

type settings struct {
	PrusaURL         string `json:"prusa_url"`
	PrusaToken       string `json:"prusa_token"`
	ObicoURL         string `json:"obico_url"`
	ObicoBrowserURL  string `json:"obico_browser_url"`
	ObicoAccessToken string `json:"obico_access_token"`
	Printers         string `json:"printers"`
	Interval         int    `json:"interval_seconds"`
}

type mapping struct {
	DeviceID string `json:"device_id"`
	Camera   string `json:"camera"`
	Token    string `json:"obico_token"`
}

var deviceID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var token = regexp.MustCompile(`^[!-~]+$`)
var code = regexp.MustCompile(`^[0-9]{6}$`)

func origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("use an HTTP or HTTPS origin without a path or credentials")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (s *settings) validate() ([]mapping, error) {
	var err error
	s.PrusaURL, err = origin(s.PrusaURL)
	if err != nil {
		return nil, fmt.Errorf("prusa_url: %w", err)
	}
	s.ObicoURL, err = origin(s.ObicoURL)
	if err != nil {
		return nil, fmt.Errorf("obico_url: %w", err)
	}
	if s.ObicoBrowserURL != "" {
		s.ObicoBrowserURL, err = origin(s.ObicoBrowserURL)
		if err != nil {
			return nil, fmt.Errorf("obico_browser_url: %w", err)
		}
	}
	if s.Interval < 5 || s.Interval > 60 || !token.MatchString(s.PrusaToken) {
		return nil, errors.New("set prusa_token and use an interval_seconds between 5 and 60")
	}
	if s.ObicoAccessToken != "" && !token.MatchString(s.ObicoAccessToken) {
		return nil, errors.New("invalid Obico API access token")
	}
	var printers []mapping
	if json.Unmarshal([]byte(s.Printers), &printers) != nil || len(printers) == 0 {
		return nil, errors.New("printers must contain a JSON array with at least one mapping")
	}
	ids, tokens := map[string]bool{}, map[string]bool{}
	for _, printer := range printers {
		if !deviceID.MatchString(printer.DeviceID) || !token.MatchString(printer.Token) ||
			ids[printer.DeviceID] || tokens[printer.Token] {
			return nil, errors.New("each printer needs a unique device_id and Obico token")
		}
		ids[printer.DeviceID], tokens[printer.Token] = true, true
	}
	return printers, nil
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error                { return os.Stdin.Close() }

func serve() error {
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	var mu sync.Mutex
	defer func() {
		cancel()
		mu.Lock()
		defer mu.Unlock()
		workers.Wait()
	}()
	line, err := (wire.Handshake{
		Protocol: wire.ProtocolMajor, MinMinor: wire.MinMinor,
		MaxMinor: wire.MaxMinor, Address: wire.StdioAddress,
	}).Line()
	if err != nil {
		return err
	}
	if _, err = os.Stdout.WriteString(line); err != nil {
		return err
	}
	configured := false
	var overview dashboard
	methods := wirerpc.Methods{
		wirerpc.MethodInfo: func(context.Context, wirerpc.Call) (any, error) {
			return routesapi.Describe(wirerpc.Info{
				Name: name, Version: version, Contract: wirerpc.Contract,
				Serves: []wirerpc.Method{wirerpc.MethodConfigure},
			}, dashboardAPI)
		},
		wirerpc.MethodShutdown: func(context.Context, wirerpc.Call) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			cancel()
			workers.Wait()
			return nil, nil
		},
		wirerpc.MethodConfigure: wirerpc.Configure(func(_ context.Context, config settings) error {
			printers, err := config.validate()
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			if configured || ctx.Err() != nil {
				return errors.New("module already configured or shutting down")
			}
			configured = true
			overview.configure(config, printers)
			for _, printer := range printers {
				workers.Go(func() {
					monitor(ctx, config, printer, func(connected bool) {
						overview.connected(printer.DeviceID, connected)
					})
				})
			}
			return nil
		}),
	}
	conn, err := wirerpc.NewConn(stdio{}, wirerpc.Options{Role: wirerpc.RoleChild, Methods: methods})
	if err != nil {
		return err
	}
	methods[routesapi.MethodServe] = routesapi.Serve(overview.routes(deviceapi.NewClient(conn), accessapi.NewClient(conn)))
	return conn.Serve(context.Background())
}

func run(args []string) error {
	if len(args) == 0 {
		return serve()
	}
	if len(args) != 2 || args[0] != "link" {
		return errors.New("usage: obico-bridge link <Obico URL>; pass the six-digit code via stdin")
	}
	base, err := origin(args[1])
	if err != nil {
		return err
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 32))
	if err != nil || !code.MatchString(strings.TrimSpace(string(input))) {
		return errors.New("expected a six-digit Obico verification code")
	}
	return link(context.Background(), base, strings.TrimSpace(string(input)))
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}
