package main

import (
	"flag"
	"io"
	"os"
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
	if err == nil {
		t.Fatal("runServer() error = nil, want load TLS certificate error")
	}
	if !strings.Contains(err.Error(), "load TLS certificate") {
		t.Fatalf("error = %q, want load TLS certificate context", err.Error())
	}
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
		if err != nil {
			t.Fatalf("runServer() error = %v, want nil", err)
		}
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
