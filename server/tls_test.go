package server_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jozefvalachovic/server/server"
)

// writeSelfSignedCert generates a self-signed cert/key pair and writes them
// to the given paths. Returns the certificate for assertion purposes.
func writeSelfSignedCert(t *testing.T, certPath, keyPath string, cn string) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cert, _ := x509.ParseCertificate(certDER)
	return cert
}

func TestCertReloader_InitialLoad(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeSelfSignedCert(t, certPath, keyPath, "initial-test")

	r, err := server.NewCertReloader(certPath, keyPath, server.WithPollInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}
	defer r.Stop()

	cert, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate")
	}
}

func TestCertReloader_ReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeSelfSignedCert(t, certPath, keyPath, "before-rotation")

	r, err := server.NewCertReloader(certPath, keyPath, server.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}
	defer r.Stop()

	// Grab the initial cert's serial number.
	before, _ := r.GetCertificate(nil)
	beforeLeaf, _ := x509.ParseCertificate(before.Certificate[0])
	beforeSerial := beforeLeaf.SerialNumber

	// Ensure file mod time changes (some filesystems have 1s granularity).
	time.Sleep(100 * time.Millisecond)

	// Write a new cert with a different serial.
	writeSelfSignedCert(t, certPath, keyPath, "after-rotation")

	// Wait for the poller to pick up the change.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cert reload")
		default:
		}
		after, _ := r.GetCertificate(nil)
		afterLeaf, _ := x509.ParseCertificate(after.Certificate[0])
		if afterLeaf.SerialNumber.Cmp(beforeSerial) != 0 {
			return // success — cert was rotated
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestCertReloader_InvalidPathsReturnError(t *testing.T) {
	_, err := server.NewCertReloader("/nonexistent/cert.pem", "/nonexistent/key.pem")
	if err == nil {
		t.Fatal("expected error for nonexistent paths")
	}
}

func TestCertReloader_EmptyPathsReturnError(t *testing.T) {
	_, err := server.NewCertReloader("", "")
	if err == nil {
		t.Fatal("expected error for empty paths")
	}
}

func TestCertReloader_StopIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeSelfSignedCert(t, certPath, keyPath, "stop-test")

	r, err := server.NewCertReloader(certPath, keyPath, server.WithPollInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}
	r.Stop()
	r.Stop() // must not panic or deadlock
}

func TestCertReloader_ConcurrentGetCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeSelfSignedCert(t, certPath, keyPath, "concurrent-test")

	r, err := server.NewCertReloader(certPath, keyPath, server.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}
	defer r.Stop()

	var wg sync.WaitGroup
	const goroutines = 50
	const iterations = 200

	// Hammer GetCertificate while cert files are being replaced.
	for range goroutines {
		wg.Go(func() {
			for range iterations {
				cert, err := r.GetCertificate(nil)
				if err != nil {
					t.Errorf("GetCertificate error: %v", err)
				}
				if cert == nil {
					t.Error("GetCertificate returned nil")
				}
			}
		})
	}

	// Simultaneously replace the cert files a few times.
	for i := range 5 {
		time.Sleep(50 * time.Millisecond)
		writeSelfSignedCert(t, certPath, keyPath, "rotation-"+string(rune('A'+i)))
	}

	wg.Wait()
}

func TestCertReloader_BadCertFileSkipsReload(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeSelfSignedCert(t, certPath, keyPath, "bad-cert-test")

	r, err := server.NewCertReloader(certPath, keyPath, server.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}
	defer r.Stop()

	before, _ := r.GetCertificate(nil)

	// Write invalid cert content — should fail reload but keep the old cert.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(certPath, []byte("not a cert"), 0600); err != nil {
		t.Fatal(err)
	}

	// Wait for a poll cycle.
	time.Sleep(200 * time.Millisecond)

	after, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	// The old certificate should still be served.
	if before != after {
		t.Error("expected old certificate to remain after bad reload")
	}
}

func TestCertReloader_WiresIntoTLSConfig(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeSelfSignedCert(t, certPath, keyPath, "tls-config-test")

	r, err := server.NewCertReloader(certPath, keyPath)
	if err != nil {
		t.Fatalf("NewCertReloader: %v", err)
	}
	defer r.Stop()

	tlsCfg := &tls.Config{
		GetCertificate: r.GetCertificate,
	}

	// Verify the callback works through the tls.Config wrapper.
	cert, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate via tls.Config: %v", err)
	}
	if cert == nil {
		t.Fatal("expected non-nil certificate from tls.Config.GetCertificate")
	}
}
