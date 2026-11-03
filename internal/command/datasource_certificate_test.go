package command

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// Cover the eight-hour capacity run and its bounded cleanup without changing TLS verification.
const performanceSourceCertificateValidity = 9 * time.Hour

func datasourceTestCertificate() ([]byte, []byte, error) {
	return datasourceTestCertificateFor(time.Hour)
}

func datasourceTestCertificateFor(validFor time.Duration) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(20261027), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(validFor),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
		DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), nil
}

func TestDatasourceCertificateCoversCapacityWindow(t *testing.T) {
	for _, validity := range []time.Duration{time.Hour, performanceSourceCertificateValidity} {
		body, _, err := datasourceTestCertificateFor(validity)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(body)
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		roots.AddCert(certificate)
		at := certificate.NotBefore.Add(time.Minute + 8*time.Hour + 30*time.Minute)
		_, err = certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost", CurrentTime: at})
		if (err == nil) != (validity == performanceSourceCertificateValidity) {
			t.Fatalf("certificate validity %s does not match the capacity execution/cleanup window: %v", validity, err)
		}
		if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost", CurrentTime: certificate.NotAfter.Add(time.Second)}); err == nil {
			t.Fatal("fixture certificate bypassed expiry validation")
		}
	}
}
