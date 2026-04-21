package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/danko-miladinovic/fort/atls"
	"github.com/danko-miladinovic/fort/atls/attestation"
	"github.com/danko-miladinovic/fort/atls/ea"
)

const (
	defaultATLSAddr         = "127.0.0.1:9443"
	kernelCmdlinePath       = "/proc/cmdline"
	verifierIPKernelParam   = "verifier_ip"
	verifierPortKernelParam = "verifier_port"
	connectRetryInterval    = 5 * time.Second
)

func main() {
	addr := resolveDialAddress(os.Getenv, os.ReadFile)
	for {
		if err := run(addr); err != nil {
			log.Printf("client connection to %s failed: %v", addr, err)
			time.Sleep(connectRetryInterval)
			continue
		}
		return
	}
}

func run(addr string) error {
	cert, err := atls.GenerateExampleCertificate()
	if err != nil {
		return err
	}
	ctx, err := ea.NewRandomContext(16)
	if err != nil {
		return err
	}
	req, err := atls.ExampleRequest(ctx)
	if err != nil {
		return err
	}
	verifyOpts, err := atls.ExampleVerifyOptions(cert)
	if err != nil {
		return err
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
		return err
	}
	defer conn.Close()

	log.Printf("EA bootstrap complete; attestation verified=%v", conn.ValidationResult != nil && conn.ValidationResult.Attestation != nil)
	if _, err := fmt.Fprintln(conn, "hello-from-client"); err != nil {
		return err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	fmt.Print(reply)
	return nil
}

func resolveDialAddress(getenv func(string) string, readFile func(string) ([]byte, error)) string {
	if addr := strings.TrimSpace(getenv("ATLS_ADDR")); addr != "" {
		return addr
	}
	if addr, ok := verifierAddressFromSource(getenv("VERIFIER_IP"), getenv("VERIFIER_PORT")); ok {
		return addr
	}

	cmdline, err := readFile(kernelCmdlinePath)
	if err != nil {
		log.Printf("unable to read %s: %v", kernelCmdlinePath, err)
		return defaultATLSAddr
	}

	params := parseKernelCmdline(string(cmdline))
	if addr, ok := verifierAddressFromSource(
		params[verifierIPKernelParam],
		params[verifierPortKernelParam],
	); ok {
		return addr
	}

	return defaultATLSAddr
}

func verifierAddressFromSource(ip, port string) (string, bool) {
	ip = strings.TrimSpace(ip)
	port = strings.TrimSpace(port)
	if ip == "" || port == "" {
		return "", false
	}
	return net.JoinHostPort(ip, port), true
}

func parseKernelCmdline(cmdline string) map[string]string {
	params := make(map[string]string)
	for _, field := range strings.Fields(cmdline) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		params[key] = value
	}
	return params
}
