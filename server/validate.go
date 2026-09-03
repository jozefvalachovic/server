package server

import (
	"errors"
	"fmt"
)

// Validate checks for obviously incorrect values in HTTPServerConfig and
// returns an aggregate error describing every problem found.
// NewHTTPServer calls Validate automatically; callers may also invoke it
// independently for early, structured feedback.
func (c *HTTPServerConfig) Validate() error {
	var errs []error

	if c.ReadTimeout < 0 {
		errs = append(errs, fmt.Errorf("ReadTimeout must be >= 0, got %s", c.ReadTimeout))
	}
	if c.ReadHeaderTimeout < 0 {
		errs = append(errs, fmt.Errorf("ReadHeaderTimeout must be >= 0, got %s", c.ReadHeaderTimeout))
	}
	if c.WriteTimeout < 0 {
		errs = append(errs, fmt.Errorf("WriteTimeout must be >= 0, got %s", c.WriteTimeout))
	}
	if c.ReadTimeout > 0 && c.WriteTimeout > 0 && c.WriteTimeout < c.ReadTimeout {
		errs = append(errs, fmt.Errorf("WriteTimeout (%s) should be >= ReadTimeout (%s)", c.WriteTimeout, c.ReadTimeout))
	}
	if c.MaxConns < 0 {
		errs = append(errs, fmt.Errorf("MaxConns must be >= 0, got %d", c.MaxConns))
	}
	if c.MaxHeaderBytes < 0 {
		errs = append(errs, fmt.Errorf("MaxHeaderBytes must be >= 0, got %d", c.MaxHeaderBytes))
	}
	if c.MaxHeaderValueCount < 0 {
		errs = append(errs, fmt.Errorf("MaxHeaderValueCount must be >= 0, got %d", c.MaxHeaderValueCount))
	}
	if c.CertReloadInterval < 0 {
		errs = append(errs, fmt.Errorf("CertReloadInterval must be >= 0, got %s", c.CertReloadInterval))
	}
	if c.Timeout != nil && c.Timeout.Timeout < 0 {
		errs = append(errs, fmt.Errorf("Timeout.Timeout must be >= 0, got %s", c.Timeout.Timeout))
	}
	if c.RateLimitConfig != nil {
		if c.RateLimitConfig.RequestsPerSecond <= 0 {
			errs = append(errs, fmt.Errorf("RateLimitConfig.RequestsPerSecond must be > 0, got %f", c.RateLimitConfig.RequestsPerSecond))
		}
		if c.RateLimitConfig.Burst <= 0 {
			errs = append(errs, fmt.Errorf("RateLimitConfig.Burst must be > 0, got %d", c.RateLimitConfig.Burst))
		}
	}
	if c.TLSConfig != nil {
		if c.TLSConfig.MinVersion == 0 {
			errs = append(errs, errors.New("TLSConfig.MinVersion must be set explicitly — stdlib default drops to TLS 1.0; set at least tls.VersionTLS12"))
		} else if c.TLSConfig.MinVersion < 0x0303 {
			errs = append(errs, errors.New("TLSConfig.MinVersion is below TLS 1.2 — not recommended"))
		}
	}
	if c.MetricsServerConfig != nil {
		if err := c.MetricsServerConfig.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Validate checks for obviously incorrect values in TCPServerConfig.
func (c *TCPServerConfig) Validate() error {
	var errs []error

	if c.ReadTimeout < 0 {
		errs = append(errs, fmt.Errorf("ReadTimeout must be >= 0, got %s", c.ReadTimeout))
	}
	if c.WriteTimeout < 0 {
		errs = append(errs, fmt.Errorf("WriteTimeout must be >= 0, got %s", c.WriteTimeout))
	}
	if c.CertReloadInterval < 0 {
		errs = append(errs, fmt.Errorf("CertReloadInterval must be >= 0, got %s", c.CertReloadInterval))
	}
	if c.RateLimitConfig != nil && c.RateLimitConfig.ConnectionsPerSecond <= 0 {
		errs = append(errs, fmt.Errorf("RateLimitConfig.ConnectionsPerSecond must be > 0, got %f", c.RateLimitConfig.ConnectionsPerSecond))
	}
	if c.TLSConfig != nil {
		if c.TLSConfig.MinVersion == 0 {
			errs = append(errs, errors.New("TLSConfig.MinVersion must be set explicitly — stdlib default drops to TLS 1.0; set at least tls.VersionTLS12"))
		} else if c.TLSConfig.MinVersion < 0x0303 {
			errs = append(errs, errors.New("TLSConfig.MinVersion is below TLS 1.2 — not recommended"))
		}
	}
	if c.MetricsServerConfig != nil {
		if err := c.MetricsServerConfig.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Validate checks for misconfigurations in the MetricsServerConfig.
func (c *MetricsServerConfig) Validate() error {
	if c == nil {
		return errors.New("MetricsServerConfig must not be nil")
	}
	var errs []error
	if c.Handler == nil {
		errs = append(errs, errors.New("MetricsServerConfig.Handler must not be nil"))
	}
	if c.TLSConfig != nil {
		if c.TLSConfig.MinVersion == 0 {
			errs = append(errs, errors.New("MetricsServerConfig.TLSConfig.MinVersion must be set explicitly"))
		} else if c.TLSConfig.MinVersion < 0x0303 {
			errs = append(errs, errors.New("MetricsServerConfig.TLSConfig.MinVersion is below TLS 1.2"))
		}
	}
	return errors.Join(errs...)
}
