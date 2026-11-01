package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

type Config struct {
	Endpoint        string
	Nonce           string
	ExpectedRelease string
	Attempts        int
	AttemptTimeout  time.Duration
	Vantage         string
}

type Result struct {
	Schema      int                `json:"schema"`
	Fixture     string             `json:"fixture"`
	Endpoint    string             `json:"endpoint"`
	Vantage     string             `json:"vantage"`
	Nonce       string             `json:"nonce"`
	Attempts    int                `json:"attempts"`
	MaxAttempts int                `json:"max_attempts"`
	StartedAt   string             `json:"started_at"`
	FinishedAt  string             `json:"finished_at"`
	Outcome     string             `json:"outcome"`
	Error       string             `json:"error,omitempty"`
	Response    *protocol.Response `json:"response,omitempty"`
}

func Probe(ctx context.Context, config Config) (Result, error) {
	if err := validateConfig(config); err != nil {
		return Result{Schema: protocol.SchemaVersion, Fixture: protocol.FixtureName, Endpoint: config.Endpoint, Vantage: config.Vantage, Outcome: "configuration_error", Error: err.Error(), MaxAttempts: protocol.MaxAttempts}, err
	}
	started := time.Now().UTC()
	result := Result{Schema: protocol.SchemaVersion, Fixture: protocol.FixtureName, Endpoint: config.Endpoint, Vantage: config.Vantage, Nonce: config.Nonce, MaxAttempts: protocol.MaxAttempts, StartedAt: started.Format(time.RFC3339Nano), Outcome: "timeout"}
	address, err := net.ResolveUDPAddr("udp", config.Endpoint)
	if err != nil {
		result.Outcome = "endpoint_error"
		result.Error = err.Error()
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return result, nil
	}
	connection, err := net.DialUDP("udp", nil, address)
	if err != nil {
		result.Outcome = "connect_error"
		result.Error = err.Error()
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		return result, nil
	}
	defer func() { _ = connection.Close() }()
	for attempt := 1; attempt <= config.Attempts; attempt++ {
		result.Attempts = attempt
		if err := probeAttempt(ctx, connection, config, &result); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result.Outcome = "canceled"
				result.Error = err.Error()
				break
			}
			if errors.Is(err, errAttemptTimeout) {
				continue
			}
			result.Outcome = "protocol_error"
			result.Error = err.Error()
			break
		}
		result.Outcome = "ok"
		break
	}
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result, nil
}

var errAttemptTimeout = errors.New("attempt timeout")

func probeAttempt(ctx context.Context, connection *net.UDPConn, config Config, result *Result) error {
	deadline := time.Now().Add(config.AttemptTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if _, err := connection.Write([]byte(config.Nonce)); err != nil {
		return err
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		return err
	}
	buffer := make([]byte, protocol.MaxDatagramBytes+1)
	n, err := connection.Read(buffer)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return errAttemptTimeout
		}
		return err
	}
	if n > protocol.MaxDatagramBytes {
		return errors.New("response exceeds packet bound")
	}
	response, err := protocol.DecodeResponse(buffer[:n])
	if err != nil {
		return err
	}
	result.Response = &response
	if config.ExpectedRelease != "" && response.Release != config.ExpectedRelease {
		return fmt.Errorf("release identity mismatch: got %q, want %q", response.Release, config.ExpectedRelease)
	}
	return nil
}

func validateConfig(config Config) error {
	if err := protocol.ValidateEndpoint(config.Endpoint); err != nil {
		return err
	}
	if err := protocol.ValidateNonce([]byte(config.Nonce)); err != nil {
		return err
	}
	if config.ExpectedRelease != "" {
		if err := protocol.ValidateIdentity(protocol.Identity{Fixture: protocol.FixtureName, Release: config.ExpectedRelease}); err != nil {
			return err
		}
	}
	if config.Attempts < 1 || config.Attempts > protocol.MaxAttempts {
		return fmt.Errorf("attempts must be between 1 and %d", protocol.MaxAttempts)
	}
	if config.AttemptTimeout <= 0 || config.AttemptTimeout > protocol.MaxTimeoutSeconds*time.Second {
		return fmt.Errorf("attempt timeout must be between 1ns and %ds", protocol.MaxTimeoutSeconds)
	}
	return protocol.ValidateVantage(config.Vantage)
}

func MarshalResult(result Result) ([]byte, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(body) > protocol.MaxJSONBodyBytes {
		return nil, errors.New("result exceeds JSON bound")
	}
	return append(body, '\n'), nil
}
