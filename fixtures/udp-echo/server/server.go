// Package server runs the disposable HTTP and UDP evidence fixture.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

const (
	DefaultHTTPAddr = ":8080"
	DefaultUDPAddr  = ":7777"
	IOTimeout       = 500 * time.Millisecond
	HTTPTimeout     = 2 * time.Second
	ShutdownTimeout = 3 * time.Second
)

type Config struct {
	HTTPAddr   string
	UDPAddr    string
	Identity   protocol.Identity
	FailHealth bool
}

type Stats struct {
	Received    uint64 `json:"received"`
	Replied     uint64 `json:"replied"`
	Rejected    uint64 `json:"rejected"`
	Oversized   uint64 `json:"oversized"`
	WriteErrors uint64 `json:"write_errors"`
}

type Server struct {
	identity    protocol.Identity
	failHealth  bool
	http        *http.Server
	httpLn      net.Listener
	udp         *net.UDPConn
	received    atomic.Uint64
	replied     atomic.Uint64
	rejected    atomic.Uint64
	oversized   atomic.Uint64
	writeErrors atomic.Uint64
	closeOne    sync.Once
	closeErr    error
}

func New(ctx context.Context, config Config) (*Server, error) {
	if err := protocol.ValidateIdentity(config.Identity); err != nil {
		return nil, err
	}
	if err := validateBind(config.HTTPAddr); err != nil {
		return nil, err
	}
	if err := validateBind(config.UDPAddr); err != nil {
		return nil, err
	}
	var listenConfig net.ListenConfig
	httpLn, err := listenConfig.Listen(ctx, "tcp", config.HTTPAddr)
	if err != nil {
		return nil, fmt.Errorf("listen HTTP: %w", err)
	}
	udpAddr, err := net.ResolveUDPAddr("udp", config.UDPAddr)
	if err != nil {
		_ = httpLn.Close()
		return nil, err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("listen UDP: %w", err)
	}
	s := &Server{identity: config.Identity, failHealth: config.FailHealth, httpLn: httpLn, udp: udp}
	s.http = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: HTTPTimeout, ReadTimeout: HTTPTimeout, WriteTimeout: HTTPTimeout, IdleTimeout: HTTPTimeout, MaxHeaderBytes: protocol.MaxHTTPHeaderBytes}
	return s, nil
}

func validateBind(value string) error {
	if len(value) > protocol.MaxEndpointBytes {
		return errors.New("bind address exceeds bound")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return errors.New("bind requires IP literal and numeric port")
	}
	if host == "" {
		host = "0.0.0.0"
	}
	address, err := netip.ParseAddrPort(net.JoinHostPort(host, port))
	if err != nil || address.Addr().Zone() != "" || address.Addr().IsMulticast() || address.Addr().Is4In6() {
		return errors.New("bind requires unzoned IP literal and numeric port")
	}
	if address.Port() != 0 && address.Port() < 1024 {
		return errors.New("bind requires an unprivileged port")
	}
	return nil
}

func (s *Server) HTTPAddr() string { return s.httpLn.Addr().String() }
func (s *Server) UDPAddr() string  { return s.udp.LocalAddr().String() }

func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); errs <- s.http.Serve(s.httpLn) }()
	go func() { defer workers.Done(); errs <- s.serveUDP(ctx) }()
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}
	closeErr := s.Close(ctx)
	workers.Wait()
	if errors.Is(runErr, http.ErrServerClosed) || errors.Is(runErr, net.ErrClosed) {
		runErr = nil
	}
	return errors.Join(runErr, closeErr)
}

func (s *Server) Close(parent context.Context) error {
	s.closeOne.Do(func() {
		_ = s.httpLn.Close()
		_ = s.udp.Close()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), ShutdownTimeout)
		defer cancel()
		s.closeErr = s.http.Shutdown(ctx)
		if s.closeErr != nil {
			_ = s.http.Close()
		}
	})
	return s.closeErr
}

func (s *Server) stats() Stats {
	return Stats{Received: s.received.Load(), Replied: s.replied.Load(), Rejected: s.rejected.Load(), Oversized: s.oversized.Load(), WriteErrors: s.writeErrors.Load()}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if err := checkRequest(w, r); err != nil {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		status := http.StatusOK
		if s.failHealth {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, struct {
			protocol.Identity
			OK bool `json:"ok"`
		}{s.identity, !s.failHealth})
	case "/stats":
		writeJSON(w, http.StatusOK, struct {
			protocol.Identity
			Stats Stats `json:"stats"`
		}{s.identity, s.stats()})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func checkRequest(w http.ResponseWriter, r *http.Request) error {
	headerBytes := len(r.Method) + len(r.RequestURI) + len(r.Proto) + len(r.Host) + 16
	for name, values := range r.Header {
		for _, value := range values {
			headerBytes += len(name) + len(value) + 4
		}
	}
	if headerBytes > protocol.MaxHTTPHeaderBytes {
		http.Error(w, "headers exceed bound", http.StatusRequestHeaderFieldsTooLarge)
		return errors.New("headers exceed bound")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxHTTPBodyBytes))
	if err != nil {
		http.Error(w, "body exceeds bound or is incomplete", http.StatusRequestEntityTooLarge)
		return err
	}
	if len(body) != 0 || r.URL.RawQuery != "" {
		http.Error(w, "body and query not accepted", http.StatusBadRequest)
		return errors.New("unexpected body or query")
	}
	return nil
}

func (s *Server) serveUDP(ctx context.Context) error {
	var buffer [protocol.MaxDatagramBytes + 1]byte
	for ctx.Err() == nil {
		if err := s.udp.SetReadDeadline(time.Now().Add(IOTimeout)); err != nil {
			return err
		}
		n, remote, err := s.udp.ReadFromUDP(buffer[:])
		if isTimeout(err) {
			continue
		}
		if err != nil {
			return err
		}
		s.reply(buffer[:n], remote)
	}
	return nil
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (s *Server) reply(packet []byte, remote *net.UDPAddr) {
	s.received.Add(1)
	if len(packet) > protocol.MaxDatagramBytes {
		s.oversized.Add(1)
		return
	}
	response, err := protocol.EncodeResponse(s.identity, packet)
	if err != nil {
		s.rejected.Add(1)
		return
	}
	if err := s.udp.SetWriteDeadline(time.Now().Add(IOTimeout)); err != nil {
		s.writeErrors.Add(1)
		return
	}
	if _, err := s.udp.WriteToUDP(response, remote); err != nil {
		s.writeErrors.Add(1)
		return
	}
	s.replied.Add(1)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > protocol.MaxHTTPBodyBytes {
		http.Error(w, "response unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
