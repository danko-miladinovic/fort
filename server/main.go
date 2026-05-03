package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/danko-miladinovic/fort/atls"
	"github.com/danko-miladinovic/fort/atls/attestation"
)

func main() {
	addr := envOrDefault("ATLS_ADDR", "127.0.0.1:9443")
	cert, err := atls.GenerateExampleCertificate()
	if err != nil {
		log.Fatal(err)
	}
	verifyOpts, err := atls.ExampleVerifyOptions(cert)
	if err != nil {
		log.Fatal(err)
	}

	ln, err := atls.Listen("tcp", addr, &atls.ServerConfig{
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			MaxVersion:   tls.VersionTLS13,
		},
		VerifyOptions: verifyOpts,
		AttestationPolicy: attestation.VerificationPolicy{
			EvidenceVerifier: atls.AcceptEvidenceVerifier{},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()

	log.Printf("atls server listening on %s", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		if atlsConn, ok := conn.(*atls.Conn); ok {
			log.Printf("client attestation verified=%v", atlsConn.ValidationResult != nil && atlsConn.ValidationResult.Attestation != nil)
		}
		go handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()
	log.Printf("accepted %s", conn.RemoteAddr())
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println(line)
		if _, err := fmt.Fprintf(conn, "echo:%s\n", line); err != nil {
			log.Printf("write failed: %v", err)
			return
		}
		if strings.EqualFold(line, "quit") {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("read failed: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
