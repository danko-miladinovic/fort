package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/danko-miladinovic/fort/atls"
)

const (
	defaultATLSAddr         = "127.0.0.1:9443"
	kernelCmdlinePath       = "/proc/cmdline"
	verifierIPKernelParam   = "verifier_ip"
	verifierPortKernelParam = "verifier_port"
	connectRetryInterval    = 5 * time.Second
	useSEVSNPAttestationEnv = "ATLS_USE_SEV_SNP_ATTESTATION"
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
	useSEVSNPAttestation, err := boolEnv(useSEVSNPAttestationEnv)
	if err != nil {
		return err
	}
	cert, err := atls.GenerateExampleCertificate()
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
		Identity:            cert,
		BuildLeafExtensions: atls.AttesterLeafExtensions(useSEVSNPAttestation),
	})
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("EA bootstrap complete; client attestation sent")
	if _, err := fmt.Fprintln(conn, "Hello World!"); err != nil {
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

func boolEnv(key string) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q", key, value)
	}
	return enabled, nil
}
