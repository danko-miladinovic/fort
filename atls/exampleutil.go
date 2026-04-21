package atls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	mathrand "math/rand"
	"time"

	"github.com/danko-miladinovic/fort/atls/attestation"
	"github.com/danko-miladinovic/fort/atls/ea"
)

type AcceptEvidenceVerifier struct{}

func (AcceptEvidenceVerifier) VerifyEvidence(evidence []byte) error { return nil }

func GenerateExampleCertificate() (tls.Certificate, error) {
	entropy := newDeterministicReader(1)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), entropy)
	if err != nil {
		return tls.Certificate{}, err
	}
	notBefore := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "atls-test"},
		NotBefore:    notBefore,
		NotAfter:     time.Date(2040, time.January, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(entropy, template, template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

func ExampleServerLeafExtensions(st *tls.ConnectionState, req *ea.AuthenticatorRequest, leaf *x509.Certificate) ([]ea.Extension, error) {
	_, aikPubHash, binding, err := attestation.ComputeBinding(st, attestation.ExporterLabelAttestation, req.Context, leaf)
	if err != nil {
		return nil, err
	}
	payloadBytes, err := attestation.MarshalPayload(attestation.Payload{
		Version:   1,
		MediaType: "application/eat+cwt",
		Evidence:  []byte("dummy-attestation-report"),
		Binder: attestation.AttestationBinder{
			ExporterLabel: attestation.ExporterLabelAttestation,
			AIKPubHash:    aikPubHash,
			Binding:       binding,
		},
	})
	if err != nil {
		return nil, err
	}
	ext, err := ea.CMWAttestationDataExtension(payloadBytes)
	if err != nil {
		return nil, err
	}
	return []ea.Extension{ext}, nil
}

func ExampleRequest(context []byte) (*ea.AuthenticatorRequest, error) {
	sigExt, err := ea.SignatureAlgorithmsExtension([]uint16{uint16(tls.ECDSAWithP256AndSHA256)})
	if err != nil {
		return nil, err
	}
	return &ea.AuthenticatorRequest{
		Type:    ea.HandshakeTypeClientCertificateRequest,
		Context: context,
		Extensions: []ea.Extension{
			sigExt,
			ea.CMWAttestationOfferExtension(),
		},
	}, nil
}

func ExampleVerifyOptions(cert tls.Certificate) (*x509.VerifyOptions, error) {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return &x509.VerifyOptions{Roots: roots}, nil
}

type deterministicReader struct {
	r *mathrand.Rand
}

func newDeterministicReader(seed int64) io.Reader {
	return &deterministicReader{r: mathrand.New(mathrand.NewSource(seed))}
}

func (r *deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r.r.Intn(256))
	}
	return len(p), nil
}
