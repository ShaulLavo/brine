//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"
)

func (caddyValidator) ReadLoaded(ctx context.Context) ([]byte, error) {
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("managed Caddy readback refused") }}
	request, err := http.NewRequestWithContext(read, http.MethodGet, "http://127.0.0.1:2019/config/", nil)
	if err != nil {
		return nil, errors.New("managed Caddy readback refused")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("managed Caddy readback refused")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("managed Caddy readback refused")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) == 0 || len(raw) > 4<<20 || read.Err() != nil {
		return nil, errors.New("managed Caddy readback refused")
	}
	return raw, nil
}

func (v caddyValidator) loggingFacts(ctx context.Context) ([]byte, string, string, error) {
	refusal := errors.New("managed Caddy source and loaded configuration differ")
	source, err := trustedRead(ctx, "/etc/caddy/Caddyfile", 1<<20)
	if err != nil {
		return nil, "", "", refusal
	}
	adapted, err := v.session.Execute(ctx, "caddy", []string{"adapt", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}, nil, false)
	if err != nil || adapted.Truncated {
		return nil, "", "", refusal
	}
	loaded, err := v.ReadLoaded(ctx)
	if err != nil {
		return nil, "", "", refusal
	}
	final, err := trustedRead(ctx, "/etc/caddy/Caddyfile", 1<<20)
	if err != nil || !bytes.Equal(source, final) {
		return nil, "", "", refusal
	}
	var fromSource, fromLoaded any
	if json.Unmarshal([]byte(adapted.Stdout), &fromSource) != nil || json.Unmarshal(loaded, &fromLoaded) != nil || !reflect.DeepEqual(fromSource, fromLoaded) {
		return nil, "", "", refusal
	}
	canonical, err := json.Marshal(fromLoaded)
	if err != nil {
		return nil, "", "", refusal
	}
	return loaded, managedServiceHash(source), managedServiceHash(canonical), nil
}
