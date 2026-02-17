package main

import (
	"strings"
	"testing"
	"time"
)

func TestRunServerTLSFlagValidationMismatch(t *testing.T) {
	baseCfg := serverConfig{
		PortalURL:    "http://localhost:4017",
		PortalAppURL: "https://*.localhost:4017",
		Bootstraps:   []string{"127.0.0.1:4017"},
		ALPN:         "http/1.1",
		Port:         0,
	}

	tests := []struct {
		name string
		cfg  serverConfig
	}{
		{
			name: "TLSCertWithoutTLSKey",
			cfg: func() serverConfig {
				cfg := baseCfg
				cfg.TLSCert = "cert.pem"
				return cfg
			}(),
		},
		{
			name: "TLSKeyWithoutTLSCert",
			cfg: func() serverConfig {
				cfg := baseCfg
				cfg.TLSKey = "key.pem"
				return cfg
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runServer(tt.cfg)
			if err == nil {
				t.Fatalf("runServer() error = nil, want mismatch error")
			}

			msg := err.Error()
			if !strings.Contains(msg, "--tls-cert") || !strings.Contains(msg, "--tls-key") {
				t.Fatalf("error = %q, want mention of --tls-cert and --tls-key", msg)
			}
		})
	}
}

func TestRunServerInvalidPortReturnsNilWithoutHanging(t *testing.T) {
	cfg := serverConfig{
		PortalURL:      "http://localhost:4017",
		PortalAppURL:   "https://*.localhost:4017",
		Bootstraps:     []string{"127.0.0.1:4017"},
		ALPN:           "http/1.1",
		Port:           -1,
		AdminSecretKey: "test-admin-secret",
	}

	done := make(chan error, 1)
	go func() {
		done <- runServer(cfg)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServer() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runServer() did not return within timeout")
	}
}
