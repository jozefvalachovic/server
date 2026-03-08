package server

import (
	"crypto/tls"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/jozefvalachovic/logger/v4"
)

// DefaultTLSConfig returns a production-hardened *tls.Config.
//
// Highlights:
//   - TLS 1.2 minimum (PCI-DSS, NIST SP 800-52r2).
//   - Only AEAD cipher suites (GCM / ChaCha20-Poly1305).
//   - Server-preferred cipher order.
//
// Callers may further customise the returned config (e.g. add client CA for mTLS).
//
//	cfg := server.DefaultTLSConfig()
//	cfg.ClientAuth = tls.RequireAndVerifyClientCert
//	cfg.ClientCAs = certPool
func DefaultTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			// TLS 1.3 cipher suites are always included by Go automatically;
			// these cover TLS 1.2 clients in preference order.
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		PreferServerCipherSuites: true,
	}
}

// setupCertReloader reads cert/key paths from the named environment variables,
// creates a CertReloader, and wires it into tlsCfg.GetCertificate.
// Both HTTP and TCP servers call this to avoid duplicating the logic.
func setupCertReloader(tlsCfg *tls.Config, certEnv, keyEnv string, interval time.Duration) (certFile, keyFile string, r *CertReloader, err error) {
	certFile = os.Getenv(certEnv)
	keyFile = os.Getenv(keyEnv)
	if certFile == "" || keyFile == "" {
		return "", "", nil, errors.New(certEnv + " or " + keyEnv + " not set")
	}
	var opts []CertReloaderOption
	if interval > 0 {
		opts = append(opts, WithPollInterval(interval))
	}
	r, err = NewCertReloader(certFile, keyFile, opts...)
	if err != nil {
		return "", "", nil, err
	}
	tlsCfg.GetCertificate = r.GetCertificate
	return certFile, keyFile, r, nil
}

// CertReloader watches a TLS certificate and key file pair and automatically
// reloads them when the files change on disk. It is safe for concurrent use
// and designed to be wired into tls.Config.GetCertificate for zero-downtime
// certificate rotation (e.g. cert-manager, ACME, manual renewal).
//
// Usage:
//
//	reloader, err := server.NewCertReloader(certPath, keyPath)
//	if err != nil { log.Fatal(err) }
//	defer reloader.Stop()
//
//	tlsCfg := server.DefaultTLSConfig()
//	tlsCfg.GetCertificate = reloader.GetCertificate
type CertReloader struct {
	certPath string
	keyPath  string

	mu   sync.RWMutex
	cert *tls.Certificate

	// modTimes track last-seen modification times to avoid unnecessary reloads.
	certModTime time.Time
	keyModTime  time.Time

	stop chan struct{}
	done chan struct{}
	log  logger.Logger
}

// NewCertReloader loads the initial certificate and starts a background
// goroutine that polls the cert/key files for changes every interval.
// The default poll interval is 30 seconds. Use WithPollInterval to override.
func NewCertReloader(certPath, keyPath string, opts ...CertReloaderOption) (*CertReloader, error) {
	if certPath == "" || keyPath == "" {
		return nil, errors.New("certPath and keyPath must be non-empty")
	}

	cfg := certReloaderConfig{pollInterval: 30 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}

	r := &CertReloader{
		certPath: certPath,
		keyPath:  keyPath,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		log:      logger.With("component", "cert-reloader"),
	}

	// Load initial certificate synchronously so startup fails fast.
	if err := r.reload(); err != nil {
		return nil, err
	}

	// Record initial mod times.
	r.recordModTimes()

	go r.pollLoop(cfg.pollInterval)
	return r, nil
}

// CertReloaderOption configures a CertReloader.
type CertReloaderOption func(*certReloaderConfig)

type certReloaderConfig struct {
	pollInterval time.Duration
}

// WithPollInterval overrides the default 30-second file poll interval.
func WithPollInterval(d time.Duration) CertReloaderOption {
	return func(c *certReloaderConfig) {
		if d > 0 {
			c.pollInterval = d
		}
	}
}

// GetCertificate is the tls.Config.GetCertificate callback.
// It returns the most recently loaded certificate.
func (r *CertReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cert, nil
}

// Stop terminates the background poll goroutine and waits for it to exit.
func (r *CertReloader) Stop() {
	select {
	case <-r.stop:
		return // already stopped
	default:
		close(r.stop)
	}
	<-r.done
}

func (r *CertReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.cert = &cert
	r.mu.Unlock()
	return nil
}

func (r *CertReloader) recordModTimes() {
	if info, err := os.Stat(r.certPath); err == nil {
		r.certModTime = info.ModTime()
	}
	if info, err := os.Stat(r.keyPath); err == nil {
		r.keyModTime = info.ModTime()
	}
}

func (r *CertReloader) filesChanged() bool {
	changed := false
	if info, err := os.Stat(r.certPath); err == nil {
		if !info.ModTime().Equal(r.certModTime) {
			changed = true
		}
	}
	if info, err := os.Stat(r.keyPath); err == nil {
		if !info.ModTime().Equal(r.keyModTime) {
			changed = true
		}
	}
	return changed
}

func (r *CertReloader) pollLoop(interval time.Duration) {
	defer close(r.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			if !r.filesChanged() {
				continue
			}
			if err := r.reload(); err != nil {
				r.log.LogError("Failed to reload TLS certificate", "error", err.Error(),
					"certPath", r.certPath, "keyPath", r.keyPath)
				continue
			}
			r.recordModTimes()
			r.log.LogInfo("TLS certificate reloaded", "certPath", r.certPath, "keyPath", r.keyPath)
		}
	}
}
