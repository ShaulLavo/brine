package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

const (
	DefaultHTTPAddr = ":8080"
	DefaultUDPAddr  = ":7777"
	ReadDeadline    = 500 * time.Millisecond
	WriteDeadline   = 500 * time.Millisecond
	HTTPReadHeader  = 2 * time.Second
	HTTPRead        = 3 * time.Second
	HTTPWrite       = 3 * time.Second
	HTTPIdle        = 5 * time.Second
	MaxHeaderBytes  = protocol.MaxJSONBodyBytes
)

type Config struct {
	HTTPAddr string
	UDPAddr  string
	Identity protocol.Identity
}

type Stats struct {
	Received    uint64 `json:"received"`
	Replied     uint64 `json:"replied"`
	Rejected    uint64 `json:"rejected"`
	Oversized   uint64 `json:"oversized"`
	WriteErrors uint64 `json:"write_errors"`
}

type counters struct {
	received    atomic.Uint64
	replied     atomic.Uint64
	rejected    atomic.Uint64
	oversized   atomic.Uint64
	writeErrors atomic.Uint64
}

func (c *counters) snapshot() Stats {
	return Stats{Received: c.received.Load(), Replied: c.replied.Load(), Rejected: c.rejected.Load(), Oversized: c.oversized.Load(), WriteErrors: c.writeErrors.Load()}
}

type Server struct {
	identity protocol.Identity
	http     *http.Server
	httpLn   net.Listener
	udp      *net.UDPConn
	counters counters
	closeOne sync.Once
}

func New(config Config) (*Server, error) {
	if config.HTTPAddr == "" {
		config.HTTPAddr = DefaultHTTPAddr
	}
	if config.UDPAddr == "" {
		config.UDPAddr = DefaultUDPAddr
	}
	if err := protocol.ValidateIdentity(config.Identity); err != nil {
		return nil, err
	}
	httpLn, err := net.Listen("tcp", config.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen HTTP: %w", err)
	}
	udpAddr, err := net.ResolveUDPAddr("udp", config.UDPAddr)
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("resolve UDP: %w", err)
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("listen UDP: %w", err)
	}
	server := &Server{identity: config.Identity, httpLn: httpLn, udp: udp}
	server.http = &http.Server{Handler: server.routes(), ReadHeaderTimeout: HTTPReadHeader, ReadTimeout: HTTPRead, WriteTimeout: HTTPWrite, IdleTimeout: HTTPIdle, MaxHeaderBytes: MaxHeaderBytes}
	return server, nil
}

func (s *Server) HTTPAddr() string { return s.httpLn.Addr().String() }

func (s *Server) UDPAddr() string { return s.udp.LocalAddr().String() }

func (s *Server) Stats() Stats { return s.counters.snapshot() }

func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 2)
	go func() { errs <- s.http.Serve(s.httpLn) }()
	go func() { errs <- s.serveUDP(ctx) }()
	select {
	case <-ctx.Done():
		return s.Close()
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return s.Close()
		}
		_ = s.Close()
		return err
	}
}

func (s *Server) Close() error {
	var result error
	s.closeOne.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), HTTPWrite)
		defer cancel()
		result = s.http.Shutdown(ctx)
		if err := s.udp.Close(); result == nil && err != nil && !errors.Is(err, net.ErrClosed) {
			result = err
		}
	})
	return result
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"schema": protocol.SchemaVersion, "fixture": s.identity.Fixture, "release": s.identity.Release, "ok": true})
	})
	mux.HandleFunc("GET /stats", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, struct {
			Schema  int    `json:"schema"`
			Fixture string `json:"fixture"`
			Release string `json:"release"`
			Stats   Stats  `json:"stats"`
		}{protocol.SchemaVersion, s.identity.Fixture, s.identity.Release, s.Stats()})
	})
	return mux
}

func (s *Server) serveUDP(ctx context.Context) error {
	buffer := make([]byte, protocol.MaxDatagramBytes+1)
	for {
		if err := s.udp.SetReadDeadline(time.Now().Add(ReadDeadline)); err != nil {
			return err
		}
		n, remote, err := s.udp.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				select {
				case <-ctx.Done():
					return nil
				default:
					continue
				}
			}
			return err
		}
		s.counters.received.Add(1)
		if n > protocol.MaxDatagramBytes {
			s.counters.oversized.Add(1)
			continue
		}
		if err := protocol.ValidateNonce(buffer[:n]); err != nil {
			s.counters.rejected.Add(1)
			continue
		}
		response, err := protocol.EncodeResponse(s.identity, buffer[:n])
		if err != nil {
			s.counters.rejected.Add(1)
			continue
		}
		if err := s.udp.SetWriteDeadline(time.Now().Add(WriteDeadline)); err != nil {
			return err
		}
		if _, err := s.udp.WriteToUDP(response, remote); err != nil {
			s.counters.writeErrors.Add(1)
			continue
		}
		s.counters.replied.Add(1)
	}
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
