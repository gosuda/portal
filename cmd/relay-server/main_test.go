package main

import (
	"flag"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
			require.Error(t, err, "runServer() should return error")

			msg := err.Error()
			require.Contains(t, msg, "--tls-cert", "error should mention --tls-cert")
			require.Contains(t, msg, "--tls-key", "error should mention --tls-key")
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
		require.NoError(t, err, "runServer() should return nil")
	case <-time.After(2 * time.Second):
		t.Fatal("runServer() did not return within timeout")
	}
}

func TestRunServerTLSCertLoadError(t *testing.T) {
	cfg := serverConfig{
		PortalURL:      "http://localhost:4017",
		PortalAppURL:   "https://*.localhost:4017",
		Bootstraps:     []string{"127.0.0.1:4017"},
		ALPN:           "http/1.1",
		Port:           0,
		AdminSecretKey: "test-admin-secret",
		TLSCert:        "missing-cert.pem",
		TLSKey:         "missing-key.pem",
	}

	err := runServer(cfg)
	require.Error(t, err, "runServer() should return error")
	require.Contains(t, err.Error(), "load TLS certificate", "error should mention load TLS certificate")
}

func TestRunServerTLSAutoInvalidPortReturnsNilWithoutHanging(t *testing.T) {
	cfg := serverConfig{
		PortalURL:      "http://localhost:4017",
		PortalAppURL:   "https://*.localhost:4017",
		Bootstraps:     []string{"127.0.0.1:4017"},
		ALPN:           "http/1.1",
		Port:           -1,
		AdminSecretKey: "test-admin-secret",
		TLSAuto:        true,
	}

	done := make(chan error, 1)
	go func() {
		done <- runServer(cfg)
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "runServer() should return nil")
	case <-time.After(2 * time.Second):
		t.Fatal("runServer() with TLS auto did not return within timeout")
	}
}

func TestMainReturnsWithoutFatalOnInvalidPort(t *testing.T) {
	oldArgs := os.Args
	oldCommandLine := flag.CommandLine
	defer func() {
		os.Args = oldArgs
		flag.CommandLine = oldCommandLine
	}()

	flag.CommandLine = flag.NewFlagSet("relay-server-test", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{
		"relay-server-test",
		"--port", "-1",
		"--portal-url", "http://localhost:4017",
		"--portal-app-url", "https://*.localhost:4017",
		"--bootstraps", "127.0.0.1:4017",
		"--admin-secret-key", "test-admin-secret",
	}

	done := make(chan struct{})
	go func() {
		main()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("main() did not return within timeout")
	}
}
