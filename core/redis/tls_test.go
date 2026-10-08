package redis

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildTLSConfigLoadsCustomCAAndClientCertificate(t *testing.T) {
	dir := t.TempDir()
	writeTestCertificate(t, filepath.Join(dir, "ca.pem"), true)
	certFile, keyFile := writeTestKeyPair(t, dir)

	got, err := buildTLSConfig(&TLSConfig{
		Enabled:    true,
		ServerName: "redis.example.test",
		CAFile:     filepath.Join(dir, "ca.pem"),
		CertFile:   certFile,
		KeyFile:    keyFile,
	})
	if err != nil {
		t.Fatalf("buildTLSConfig() error = %v", err)
	}
	if got.ServerName != "redis.example.test" || got.MinVersion == 0 || len(got.Certificates) != 1 || len(got.RootCAs.Subjects()) == 0 {
		t.Fatalf("unexpected TLS config: %+v", got)
	}
}

func TestBuildTLSConfigRejectsInvalidCA(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(caFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTLSConfig(&TLSConfig{Enabled: true, CAFile: caFile}); err == nil {
		t.Fatal("buildTLSConfig() error = nil, want invalid CA error")
	}
}

func writeTestCertificate(t *testing.T, path string, isCA bool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "redis test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, path, "CERTIFICATE", der, 0o600)
}

func writeTestKeyPair(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "redis client"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(dir, "client.pem")
	keyFile := filepath.Join(dir, "client.key")
	writePEM(t, certFile, "CERTIFICATE", der, 0o600)
	writePEM(t, keyFile, "PRIVATE KEY", keyDER, 0o600)
	return certFile, keyFile
}

func writePEM(t *testing.T, path, kind string, der []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), mode); err != nil {
		t.Fatal(err)
	}
}
