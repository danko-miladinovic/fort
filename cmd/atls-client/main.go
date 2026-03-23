package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"os"

	"github.com/tls-attestation/attestation-exported-authenticators-go/atls"
	"github.com/tls-attestation/attestation-exported-authenticators-go/attestation"
	"github.com/tls-attestation/attestation-exported-authenticators-go/ea"
)

func main() {
	addr := envOrDefault("ATLS_ADDR", "127.0.0.1:9443")
	cert, err := atls.GenerateExampleCertificate()
	if err != nil {
		log.Fatal(err)
	}
	ctx, err := ea.NewRandomContext(16)
	if err != nil {
		log.Fatal(err)
	}
	req, err := atls.ExampleRequest(ctx)
	if err != nil {
		log.Fatal(err)
	}
	verifyOpts, err := atls.ExampleVerifyOptions(cert)
	if err != nil {
		log.Fatal(err)
	}

	conn, err := atls.Dial("tcp", addr, &atls.ClientConfig{
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "localhost",
			MinVersion:         tls.VersionTLS13,
			MaxVersion:         tls.VersionTLS13,
		},
		VerifyOptions: verifyOpts,
		Request:       req,
		AttestationPolicy: attestation.VerificationPolicy{
			EvidenceVerifier: atls.AcceptEvidenceVerifier{},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	log.Printf("EA bootstrap complete; attestation verified=%v", conn.ValidationResult != nil && conn.ValidationResult.Attestation != nil)
	if _, err := fmt.Fprintln(conn, "hello-from-client"); err != nil {
		log.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(reply)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
