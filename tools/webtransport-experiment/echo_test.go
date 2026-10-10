package experiment

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
)

const origin = "https://game.example.invalid"
const budget = 15 * time.Second
const nonceSize = 32

func TestDirectEcho(t *testing.T) {
	serverTLS, clientTLS := certificates(t)
	packet := listen(t)
	result := make(chan error, 1)
	server := &webtransport.Server{
		H3:          &http3.Server{TLSConfig: serverTLS},
		Config:      flowLimits(),
		CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == origin },
	}
	server.H3.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wt" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodConnect || r.Proto != "webtransport-h3" {
			http.Error(w, "unexpected CONNECT protocol", http.StatusBadRequest)
			result <- errors.New("client did not send current WebTransport extended CONNECT")
			return
		}
		session, err := server.Upgrade(w, r)
		if err != nil {
			http.Error(w, "admission refused", http.StatusForbidden)
			result <- err
			return
		}
		settings := w.(http3.Settingser).Settings()
		if !settings.EnableDatagrams || settings.Other[0x2c7cf000] != 1 || settings.Other[0x2b64] != 1 || settings.Other[0x2b65] != 1 || settings.Other[0x2b61] != 1024 {
			result <- errors.New("client did not advertise current WebTransport and bounded session flow-control settings")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		result <- echo(ctx, session)
	})
	serve(t, func() error { return server.Serve(packet) }, server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	client := &webtransport.Transport{TLSClientConfig: clientTLS, Config: flowLimits()}
	response, session, err := client.Dial(ctx, "https://"+packet.LocalAddr().String()+"/wt", http.Header{"Origin": {origin}})
	defer func() { _ = client.Close() }()
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.CloseWithError(0, "done") }()
	if response.StatusCode != http.StatusOK || session.SessionState().ConnectionState.TLS.NegotiatedProtocol != "h3" {
		t.Fatal("session did not negotiate HTTP/3 CONNECT admission")
	}
	t.Log("session admitted; ALPN h3; pinned draft-16 implementation (no numeric draft negotiation); current WebTransport and session flow-control settings asserted")
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if err := stream.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write(nonce); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	assertNonce(t, stream, nonce)
	t.Log("bidirectional nonce echo passed")
	uni, err := session.AcceptUniStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := uni.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	assertNonce(t, uni, nonce)
	t.Log("server-to-client unidirectional nonce echo passed")
	if err := session.SendDatagram(nonce); err != nil {
		t.Fatal(err)
	}
	got, err := session.ReceiveDatagram(ctx)
	if err != nil || !bytes.Equal(got, nonce) {
		t.Fatalf("datagram echo mismatch: %v", err)
	}
	t.Log("datagram nonce echo passed")
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func echo(ctx context.Context, session *webtransport.Session) error {
	stream, err := session.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept bidi: %w", err)
	}
	deadline, _ := ctx.Deadline()
	if err := stream.SetDeadline(deadline); err != nil {
		return err
	}
	message := make([]byte, nonceSize+1)
	n, err := io.ReadFull(stream, message)
	if n != nonceSize || !errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.Join(fmt.Errorf("bidi request must contain exactly %d bytes and FIN: read %d", nonceSize, n), err)
	}
	nonce := message[:n]
	if _, err := stream.Write(nonce); err != nil {
		return err
	}
	if err := stream.Close(); err != nil {
		return err
	}
	uni, err := session.OpenUniStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open uni: %w", err)
	}
	if err := uni.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if _, err := uni.Write(nonce); err != nil {
		return err
	}
	if err := uni.Close(); err != nil {
		return err
	}
	datagram, err := session.ReceiveDatagram(ctx)
	if err != nil {
		return fmt.Errorf("receive datagram: %w", err)
	}
	if !bytes.Equal(datagram, nonce) {
		return errors.New("datagram request must match bidi nonce")
	}
	return session.SendDatagram(datagram)
}

func TestHTTP3OnlyRefusesWebTransport(t *testing.T) {
	serverTLS, clientTLS := certificates(t)
	packet := listen(t)
	server := &http3.Server{
		TLSConfig:       serverTLS,
		EnableDatagrams: true,
		QUICConfig:      &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
		Handler:         http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	}
	serve(t, func() error { return server.Serve(packet) }, server.Close)
	transport := &http3.Transport{TLSClientConfig: clientTLS}
	defer func() { _ = transport.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	url := "https://" + packet.LocalAddr().String() + "/wt"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 3 {
		t.Fatal("negative control is not a working HTTP/3 endpoint")
	}
	client := &webtransport.Transport{TLSClientConfig: clientTLS}
	admission, session, err := client.Dial(ctx, url, http.Header{"Origin": {origin}})
	defer func() { _ = client.Close() }()
	if admission != nil && admission.Body != nil {
		defer func() { _ = admission.Body.Close() }()
	}
	if session != nil {
		_ = session.CloseWithError(0, "unexpected admission")
		t.Fatal("HTTP/3-only endpoint admitted WebTransport")
	}
	var refused *webtransport.RequirementsNotMetError
	if !errors.As(err, &refused) || refused.Message != "server didn't enable WebTransport" {
		t.Fatalf("expected missing WebTransport settings refusal, got %v", err)
	}
	t.Logf("HTTP/3 GET 200 succeeded; WebTransport admission refused: %s", refused.Message)
}

func flowLimits() *webtransport.Config {
	return &webtransport.Config{MaxIncomingStreams: 1, MaxIncomingUniStreams: 1, MaxIncomingData: 1024}
}

func assertNonce(t *testing.T, reader io.Reader, want []byte) {
	t.Helper()
	got := make([]byte, nonceSize+1)
	n, err := io.ReadFull(reader, got)
	if !errors.Is(err, io.ErrUnexpectedEOF) || n != nonceSize || !bytes.Equal(got[:n], want) {
		t.Fatalf("stream echo must be exactly %d bytes: read %d, error %v", nonceSize, n, err)
	}
}

func listen(t *testing.T) net.PacketConn {
	t.Helper()
	packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packet.Close() })
	return packet
}

func serve(t *testing.T, run func() error, closeServer func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	t.Cleanup(func() {
		if err := closeServer(); err != nil {
			t.Error(err)
		}
		select {
		case <-done:
		case <-time.After(budget):
			t.Error("server did not stop under deadline")
		}
	})
}

func certificates(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local protocol experiment"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		&tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}, RootCAs: roots}
}
