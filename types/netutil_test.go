//revive:disable:var-naming
package types

import (
	"strings"
	"testing"
)

func TestNormalizeTargetAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "host and port",
			in:   "localhost:3000",
			want: "localhost:3000",
		},
		{
			name: "url with scheme",
			in:   "http://localhost:3000",
			want: "localhost:3000",
		},
		{
			name:    "url missing host",
			in:      "http:///only-path",
			wantErr: true,
		},
		{
			name:    "empty",
			in:      " ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeTargetAddr(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tt.in)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.in, err)
			}

			if got != tt.want {
				t.Fatalf("NormalizeTargetAddr(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeServiceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "simple", in: "my-app", want: "my-app", ok: true},
		{name: "trim and lowercase", in: "  My-App  ", want: "my-app", ok: true},
		{name: "strip wildcard and trailing dot", in: "*.Service.", want: "service", ok: true},
		{name: "empty", in: " ", ok: false},
		{name: "contains dot", in: "api.v1", ok: false},
		{name: "contains underscore", in: "api_v1", ok: false},
		{name: "leading hyphen", in: "-api", ok: false},
		{name: "trailing hyphen", in: "api-", ok: false},
		{name: "too long", in: strings.Repeat("a", 64), ok: false},
		{name: "contains spaces", in: "api v1", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := NormalizeServiceName(tt.in)
			if ok != tt.ok {
				t.Fatalf("NormalizeServiceName(%q) ok=%v, want %v", tt.in, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("NormalizeServiceName(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPortalHostDerivationConsistency(t *testing.T) {
	t.Parallel()

	portalURL := "https://relay.edge.example.com:8443/path"

	if got := PortalRootHost(portalURL); got != "relay.edge.example.com" {
		t.Fatalf("PortalRootHost(%q)=%q, want %q", portalURL, got, "relay.edge.example.com")
	}
	if got := PortalHostPort(portalURL); got != "relay.edge.example.com:8443" {
		t.Fatalf("PortalHostPort(%q)=%q, want %q", portalURL, got, "relay.edge.example.com:8443")
	}
	if got := DefaultAppPattern(portalURL); got != "*.relay.edge.example.com:8443" {
		t.Fatalf("DefaultAppPattern(%q)=%q, want %q", portalURL, got, "*.relay.edge.example.com:8443")
	}
	if got := BuildSNIName("Api-Gateway", portalURL); got != "api-gateway.relay.edge.example.com" {
		t.Fatalf("BuildSNIName()=%q, want %q", got, "api-gateway.relay.edge.example.com")
	}
}

func TestServicePublicURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		portalURL string
		service   string
		want      string
	}{
		{
			name:      "preserve explicit scheme and root host",
			portalURL: "https://portal.example.com:4017/admin",
			service:   "my-app",
			want:      "https://my-app.portal.example.com",
		},
		{
			name:      "default scheme for host-only portal URL",
			portalURL: "portal.example.com",
			service:   "My-App",
			want:      "https://my-app.portal.example.com",
		},
		{
			name:      "invalid service returns empty",
			portalURL: "https://portal.example.com",
			service:   "my_app",
			want:      "",
		},
		{
			name:      "invalid portal returns empty",
			portalURL: "",
			service:   "my-app",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ServicePublicURL(tt.portalURL, tt.service)
			if got != tt.want {
				t.Fatalf("ServicePublicURL(%q, %q)=%q, want %q", tt.portalURL, tt.service, got, tt.want)
			}
		})
	}
}

func TestNonApexPortalRoundTrip(t *testing.T) {
	t.Parallel()

	const (
		portalURL         = "https://portal.edge.example.com:8443"
		leaseNameInput    = "My-App"
		expectedLeaseName = "my-app"
		expectedHost      = "my-app.portal.edge.example.com"
		expectedPublicURL = "https://my-app.portal.edge.example.com"
	)

	if got := BuildSNIName(leaseNameInput, portalURL); got != expectedHost {
		t.Fatalf("BuildSNIName(%q, %q)=%q, want %q", leaseNameInput, portalURL, got, expectedHost)
	}
	if got := ServicePublicURL(portalURL, leaseNameInput); got != expectedPublicURL {
		t.Fatalf("ServicePublicURL(%q, %q)=%q, want %q", portalURL, leaseNameInput, got, expectedPublicURL)
	}

	appURLs := []string{
		portalURL,
		"portal.edge.example.com:8443",
		"*.portal.edge.example.com:8443",
	}
	for _, appURL := range appURLs {
		t.Run(appURL, func(t *testing.T) {
			t.Parallel()

			gotLeaseName, ok := LeaseNameFromHost(expectedHost, appURL)
			if !ok {
				t.Fatalf("LeaseNameFromHost(%q, %q) expected success", expectedHost, appURL)
			}
			if gotLeaseName != expectedLeaseName {
				t.Fatalf("LeaseNameFromHost(%q, %q)=%q, want %q", expectedHost, appURL, gotLeaseName, expectedLeaseName)
			}
		})
	}

	if gotLeaseName, ok := LeaseNameFromHost("portal.edge.example.com", portalURL); ok {
		t.Fatalf("LeaseNameFromHost() unexpected success for apex host, got %q", gotLeaseName)
	}
}

func TestIsValidLeaseNameUsesServiceValidation(t *testing.T) {
	t.Parallel()

	if !IsValidLeaseName("my-app") {
		t.Fatalf("expected valid lease name")
	}
	if IsValidLeaseName("my_app") {
		t.Fatalf("expected underscore to be invalid for DNS-safe lease names")
	}
}

func TestParsePortalAddressPreservesFullRootHost(t *testing.T) {
	t.Parallel()

	scheme, rootHost, hostPort, ok := parsePortalAddress("https://portal.edge.example.com:8443/path", "http")
	if !ok {
		t.Fatalf("expected parsePortalAddress success")
	}
	if scheme != "https" {
		t.Fatalf("scheme=%q, want https", scheme)
	}
	if rootHost != "portal.edge.example.com" {
		t.Fatalf("rootHost=%q, want portal.edge.example.com", rootHost)
	}
	if hostPort != "portal.edge.example.com:8443" {
		t.Fatalf("hostPort=%q, want portal.edge.example.com:8443", hostPort)
	}
}

func TestParsePortalAddressHostOnlyUsesFallbackScheme(t *testing.T) {
	t.Parallel()

	scheme, rootHost, hostPort, ok := parsePortalAddress("portal.edge.example.com:9443", "http")
	if !ok {
		t.Fatalf("expected parsePortalAddress success")
	}
	if scheme != "http" {
		t.Fatalf("scheme=%q, want http", scheme)
	}
	if rootHost != "portal.edge.example.com" {
		t.Fatalf("rootHost=%q, want portal.edge.example.com", rootHost)
	}
	if hostPort != "portal.edge.example.com:9443" {
		t.Fatalf("hostPort=%q, want portal.edge.example.com:9443", hostPort)
	}
}

func TestParsePortalAddressNormalizesTrailingDotAndCase(t *testing.T) {
	t.Parallel()

	scheme, rootHost, hostPort, ok := parsePortalAddress("HTTPS://Portal.Edge.Example.COM.:7443", "http")
	if !ok {
		t.Fatalf("expected parsePortalAddress success")
	}
	if scheme != "https" {
		t.Fatalf("scheme=%q, want https", scheme)
	}
	if rootHost != "portal.edge.example.com" {
		t.Fatalf("rootHost=%q, want portal.edge.example.com", rootHost)
	}
	if hostPort != "portal.edge.example.com:7443" {
		t.Fatalf("hostPort=%q, want portal.edge.example.com:7443", hostPort)
	}
}

func TestBuildSNINameFallbackNormalizesRootHost(t *testing.T) {
	t.Parallel()

	got := BuildSNIName("Api-Gateway", " *.Portal.Edge.Example.COM. ")
	want := "api-gateway.portal.edge.example.com"
	if got != want {
		t.Fatalf("BuildSNIName fallback=%q, want %q", got, want)
	}
}

func TestIsLocalhost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "localhost", host: "localhost", want: true},
		{name: "localhost with port", host: "localhost:4017", want: true},
		{name: "subdomain localhost", host: "portal.localhost", want: true},
		{name: "ipv4 loopback", host: "127.0.0.1", want: true},
		{name: "ipv4 loopback with port", host: "127.0.0.1:4017", want: true},
		{name: "ipv6 loopback", host: "::1", want: true},
		{name: "ipv6 loopback with port", host: "[::1]:4017", want: true},
		{name: "public host", host: "example.com", want: false},
		{name: "public ip", host: "8.8.8.8", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := IsLocalhost(tt.host); got != tt.want {
				t.Fatalf("IsLocalhostHost(%q)=%v, want %v", tt.host, got, tt.want)
			}
		})
	}
}
