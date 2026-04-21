package atls

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"testing"

	"github.com/danko-miladinovic/fort/atls/attestation"
	"github.com/danko-miladinovic/fort/atls/ea"
)

func TestClientServerBootstrapOverCryptoTLS(t *testing.T) {
	cert, err := GenerateExampleCertificate()
	if err != nil {
		t.Fatal(err)
	}
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
	}
	clientTLS := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "localhost",
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
	}

	serverCfg := &ServerConfig{
		TLSConfig:           serverTLS,
		Identity:            cert,
		BuildLeafExtensions: ExampleServerLeafExtensions,
	}

	a, b := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		tlsConn := tls.Server(a, serverTLS.Clone())
		conn, err := Server(tlsConn, serverCfg)
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		if _, err := conn.Read(buf); err != nil {
			serverErr <- err
			return
		}
		if !bytes.Equal(buf, []byte("ping")) {
			serverErr <- fmt.Errorf("unexpected client payload %q", buf)
			return
		}
		_, err = conn.Write([]byte("pong"))
		serverErr <- err
	}()

	ctx, err := ea.NewRandomContext(16)
	if err != nil {
		t.Fatal(err)
	}
	req, err := ExampleRequest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	verifyOpts, err := ExampleVerifyOptions(cert)
	if err != nil {
		t.Fatal(err)
	}
	clientConn, err := Client(tls.Client(b, clientTLS.Clone()), &ClientConfig{
		TLSConfig:     clientTLS,
		VerifyOptions: verifyOpts,
		Request:       req,
		AttestationPolicy: attestation.VerificationPolicy{
			EvidenceVerifier: AcceptEvidenceVerifier{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	if clientConn.ValidationResult == nil || clientConn.ValidationResult.Attestation == nil {
		t.Fatalf("expected attestation validation result")
	}
	if _, err := clientConn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := clientConn.Read(reply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply, []byte("pong")) {
		t.Fatalf("unexpected reply %q", reply)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}
