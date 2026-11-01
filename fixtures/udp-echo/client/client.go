// Package client records bounded nonce exchanges independently of Brine status.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

type Config struct {
	Endpoint        string
	Nonce           string
	ExpectedRelease string
	Attempts        int
	AttemptTimeout  time.Duration
	OverallTimeout  time.Duration
	Vantage         string
}

type Result struct {
	Schema           int                `json:"schema"`
	Fixture          string             `json:"fixture"`
	Endpoint         string             `json:"endpoint,omitempty"`
	LocalAddress     string             `json:"local_address,omitempty"`
	Vantage          string             `json:"vantage,omitempty"`
	Nonce            string             `json:"nonce,omitempty"`
	ExpectedRelease  string             `json:"expected_release,omitempty"`
	Attempts         int                `json:"attempts"`
	AttemptTimeoutMs int64              `json:"attempt_timeout_ms"`
	OverallTimeoutMs int64              `json:"overall_timeout_ms"`
	StartedAt        time.Time          `json:"started_at"`
	FinishedAt       time.Time          `json:"finished_at"`
	Outcome          string             `json:"outcome"`
	Error            string             `json:"error,omitempty"`
	Response         *protocol.Response `json:"response,omitempty"`
}

var errAttemptTimeout = errors.New("attempt timeout")

func Probe(parent context.Context, config Config) (Result, error) {
	result := Result{Schema: protocol.SchemaVersion, Fixture: protocol.FixtureName, StartedAt: time.Now().UTC()}
	endpoint, err := validateConfig(config)
	if err != nil {
		return finish(result, "configuration_error", err), err
	}
	result.Endpoint, result.Vantage, result.Nonce = endpoint.String(), config.Vantage, config.Nonce
	result.ExpectedRelease = config.ExpectedRelease
	result.AttemptTimeoutMs = config.AttemptTimeout.Milliseconds()
	result.OverallTimeoutMs = config.OverallTimeout.Milliseconds()
	ctx, cancel := context.WithTimeout(parent, config.OverallTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return finish(result, contextOutcome(err), err), nil
	}
	connection, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		return finish(result, "connect_error", errors.New("UDP dial failed")), nil
	}
	defer func() { _ = connection.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	result.LocalAddress = connection.LocalAddr().String()
	return exchange(ctx, connection, config, result), nil
}

func exchange(ctx context.Context, connection *net.UDPConn, config Config, result Result) Result {
	for attempt := 1; attempt <= config.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return finish(result, contextOutcome(err), err)
		}
		result.Attempts = attempt
		response, err := probeAttempt(ctx, connection, config)
		if response.Fixture != "" {
			result.Response = &response
		}
		if err == nil {
			return finish(result, "ok", nil)
		}
		if errors.Is(err, errAttemptTimeout) {
			continue
		}
		return finish(result, errorOutcome(err), err)
	}
	return finish(result, "timeout", errAttemptTimeout)
}

func finish(result Result, outcome string, err error) Result {
	result.Outcome = outcome
	if err != nil {
		result.Error = err.Error()
	}
	result.FinishedAt = time.Now().UTC()
	return result
}

func probeAttempt(ctx context.Context, connection *net.UDPConn, config Config) (protocol.Response, error) {
	deadline := time.Now().Add(config.AttemptTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return protocol.Response{}, classifyNetworkError(ctx, err)
	}
	if _, err := connection.Write([]byte(config.Nonce)); err != nil {
		return protocol.Response{}, classifyNetworkError(ctx, err)
	}
	var buffer [protocol.MaxResponseBytes + 1]byte
	n, err := connection.Read(buffer[:])
	if err != nil {
		return protocol.Response{}, classifyNetworkError(ctx, err)
	}
	response, err := protocol.DecodeResponse(buffer[:n])
	if err != nil {
		return protocol.Response{}, err
	}
	if response.Nonce != config.Nonce {
		return response, errors.New("response nonce mismatch")
	}
	if response.Release != config.ExpectedRelease {
		return response, errors.New("response release mismatch")
	}
	return response, nil
}

func classifyNetworkError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return syscall.ECONNREFUSED
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errAttemptTimeout
	}
	return errors.New("UDP I/O failed")
}

func contextOutcome(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "canceled"
}

func errorOutcome(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextOutcome(err)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	return "protocol_error"
}

func validateConfig(config Config) (netip.AddrPort, error) {
	endpoint, err := protocol.Endpoint(config.Endpoint)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if err := protocol.ValidateNonce([]byte(config.Nonce)); err != nil {
		return netip.AddrPort{}, err
	}
	if err := protocol.ValidateToken("expected release", config.ExpectedRelease, protocol.MaxReleaseBytes); err != nil {
		return netip.AddrPort{}, err
	}
	if err := protocol.ValidateToken("vantage", config.Vantage, protocol.MaxVantageBytes); err != nil {
		return netip.AddrPort{}, err
	}
	if config.Attempts < 1 || config.Attempts > protocol.MaxAttempts {
		return netip.AddrPort{}, errors.New("attempts must be between 1 and 3")
	}
	if config.AttemptTimeout < time.Millisecond || config.AttemptTimeout > protocol.MaxTimeoutSeconds*time.Second {
		return netip.AddrPort{}, errors.New("attempt timeout must be between 1ms and 5s")
	}
	if config.OverallTimeout < time.Millisecond || config.OverallTimeout > protocol.MaxOverallSeconds*time.Second {
		return netip.AddrPort{}, errors.New("overall timeout must be between 1ms and 15s")
	}
	return endpoint, nil
}

func MarshalResult(result Result) ([]byte, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(body)+1 > protocol.MaxHTTPBodyBytes {
		return nil, errors.New("result exceeds JSON bound")
	}
	return append(body, '\n'), nil
}
