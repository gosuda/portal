package sdk

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/types"
)

func TestNormalizeRelayAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "localhost subdomain to localhost", in: "https://demo-app.localhost:4017", want: "https://localhost:4017"},
		{name: "http base rejected", in: "http://example.com", wantErr: true},
		{name: "https base", in: "https://example.com/", want: "https://example.com"},
		{name: "bare host", in: "localhost:4017", want: "https://localhost:4017"},
		{name: "invalid ws scheme", in: "ws://localhost:4017", wantErr: true},
		{name: "invalid wss scheme", in: "wss://example.com", wantErr: true},
		{name: "invalid relay path", in: "https://localhost:4017/relay", wantErr: true},
		{name: "invalid scheme", in: "ftp://example.com", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := types.NormalizeRelayAPIURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got none", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("types.NormalizeRelayAPIURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRelayConnectURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		relayAddr  string
		wantScheme string
		wantHost   string
		wantErr    bool
	}{
		{
			name:      "http relay URL rejected",
			relayAddr: "http://localhost:4017",
			wantErr:   true,
		},
		{
			name:       "https relay URL",
			relayAddr:  "https://relay.example.com",
			wantScheme: "https",
			wantHost:   "relay.example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := relayConnectURL(tt.relayAddr, "lease-1", "token-1")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for relay %q", tt.relayAddr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}
			if parsed.Scheme != tt.wantScheme {
				t.Fatalf("unexpected URL scheme: got %q want %q", parsed.Scheme, tt.wantScheme)
			}
			if parsed.Host != tt.wantHost {
				t.Fatalf("unexpected URL host: got %q want %q", parsed.Host, tt.wantHost)
			}
			if parsed.Path != types.PathSDKConnect {
				t.Fatalf("unexpected URL path: got %q want %q", parsed.Path, types.PathSDKConnect)
			}
			if parsed.Query().Get("lease_id") != "lease-1" {
				t.Fatalf("missing lease_id in URL: %q", got)
			}
			if parsed.Query().Get("token") != "" {
				t.Fatalf("token must not be present in URL query: %q", got)
			}
		})
	}

	if _, err := relayConnectURL("http://localhost:4017", "", "token-1"); err == nil {
		t.Fatal("expected error for empty lease ID")
	}
	if _, err := relayConnectURL("http://localhost:4017", "lease-1", ""); err == nil {
		t.Fatal("expected error for empty token")
	}
	if _, err := relayConnectURL("ws://localhost:4017", "lease-1", "token-1"); err == nil {
		t.Fatal("expected error for unsupported ws scheme")
	}
}

func TestBuildReverseConnectRequest(t *testing.T) {
	t.Parallel()

	connectURL, err := relayConnectURL("https://relay.example.com", "lease-1", "token-1")
	if err != nil {
		t.Fatalf("relayConnectURL returned error: %v", err)
	}

	u, err := url.Parse(connectURL)
	if err != nil {
		t.Fatalf("parse connect URL: %v", err)
	}

	req, err := buildReverseConnectRequest(u, " token-1 ")
	if err != nil {
		t.Fatalf("buildReverseConnectRequest returned error: %v", err)
	}

	if req.Method != http.MethodGet {
		t.Fatalf("unexpected request method: got %q want %q", req.Method, http.MethodGet)
	}
	if req.Host != "relay.example.com" {
		t.Fatalf("unexpected host header: got %q want %q", req.Host, "relay.example.com")
	}
	if req.URL.Path != types.PathSDKConnect {
		t.Fatalf("unexpected request path: got %q want %q", req.URL.Path, types.PathSDKConnect)
	}
	if req.URL.Query().Get("lease_id") != "lease-1" {
		t.Fatalf("unexpected lease_id query: %q", req.URL.Query().Get("lease_id"))
	}
	if req.URL.Query().Get("token") != "" {
		t.Fatalf("token must not be present in query: %q", req.URL.RawQuery)
	}
	if got := req.Header.Get(types.ReverseConnectTokenHeader); got != "token-1" {
		t.Fatalf("unexpected reverse token header: got %q want %q", got, "token-1")
	}
}

func TestOpenReverseConnection_RejectsNonHTTPSRelay(t *testing.T) {
	t.Parallel()

	l := &Listener{
		relayAddr:          "http://localhost:4017",
		lease:              &types.Lease{ID: "lease-1", ReverseToken: "token-1"},
		reverseDialTimeout: 2 * time.Second,
		stopCh:             make(chan struct{}),
	}

	conn, err := l.openReverseConnection()
	if err != nil {
		if !strings.Contains(err.Error(), "https") {
			t.Fatalf("expected https scheme error, got: %v", err)
		}
		return
	}
	_ = conn.Close()
	t.Fatal("expected openReverseConnection to reject non-https relay")
}

func TestOpenReverseConnection_StopUnblocksTLSHandshake(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		accepted <- struct{}{}
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}()

	l := &Listener{
		relayAddr:          "https://" + ln.Addr().String(),
		lease:              &types.Lease{ID: "lease-1", ReverseToken: "token-1"},
		reverseDialTimeout: 5 * time.Second,
		stopCh:             make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() {
		_, openErr := l.openReverseConnection()
		done <- openErr
	}()

	select {
	case <-accepted:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for reverse dial accept")
	}

	close(l.stopCh)

	select {
	case openErr := <-done:
		if openErr == nil {
			t.Fatal("expected stop-aware openReverseConnection error")
		}
		if !errors.Is(openErr, net.ErrClosed) {
			t.Fatalf("expected net.ErrClosed, got: %v", openErr)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("openReverseConnection did not unblock after stop")
	}
}

func TestWriteReverseConnectRequest_RespectsWriteDeadline(t *testing.T) {
	t.Parallel()

	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	requestURL, err := url.Parse("https://relay.example.com" + types.PathSDKConnect + "?lease_id=lease-1")
	if err != nil {
		t.Fatalf("parse request URL: %v", err)
	}

	l := &Listener{
		lease:              &types.Lease{ReverseToken: "token-1"},
		reverseDialTimeout: 25 * time.Millisecond,
		stopCh:             make(chan struct{}),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- l.writeReverseConnectRequest(local, requestURL)
	}()

	select {
	case writeErr := <-errCh:
		if writeErr == nil {
			t.Fatal("expected write deadline error")
		}
		var netErr net.Error
		if !errors.As(writeErr, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected timeout error, got: %v", writeErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for write result")
	}
}

func TestReadReverseConnectResponse_RespectsReadDeadline(t *testing.T) {
	t.Parallel()

	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	l := &Listener{
		reverseDialTimeout: 25 * time.Millisecond,
		stopCh:             make(chan struct{}),
	}

	errCh := make(chan error, 1)
	go func() {
		_, readErr := l.readReverseConnectResponse(local)
		errCh <- readErr
	}()

	select {
	case readErr := <-errCh:
		if readErr == nil {
			t.Fatal("expected read deadline error")
		}
		var netErr net.Error
		if !errors.As(readErr, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected timeout error, got: %v", readErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for read result")
	}
}

func TestParseReverseConnectRejection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		code string
		want string
	}{
		{
			name: "envelope with code and message",
			body: `{"ok":false,"error":{"code":"ip_banned","message":"ip is banned"}}`,
			code: "ip_banned",
			want: "ip is banned (code=ip_banned)",
		},
		{
			name: "envelope with message only",
			body: `{"ok":false,"error":{"code":"","message":"missing lease_id"}}`,
			code: "",
			want: "missing lease_id",
		},
		{
			name: "plain text body",
			body: " unauthorized reverse connect ",
			code: "",
			want: "unauthorized reverse connect",
		},
		{
			name: "empty body",
			body: "  ",
			code: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, detail := parseReverseConnectRejection([]byte(tt.body))
			if code != tt.code {
				t.Fatalf("parseReverseConnectRejection(%q) code=%q, want %q", tt.body, code, tt.code)
			}
			if detail != tt.want {
				t.Fatalf("parseReverseConnectRejection(%q) detail=%q, want %q", tt.body, detail, tt.want)
			}
		})
	}
}

func TestReadReverseConnectResponseParsesEnvelopeError(t *testing.T) {
	t.Parallel()

	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	l := &Listener{
		reverseDialTimeout: 500 * time.Millisecond,
		stopCh:             make(chan struct{}),
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := l.readReverseConnectResponse(local)
		errCh <- err
	}()

	body := `{"ok":false,"error":{"code":"ip_banned","message":"ip is banned"}}`
	response := fmt.Sprintf(
		"HTTP/1.1 403 Forbidden\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		len(body),
		body,
	)
	if _, err := io.WriteString(peer, response); err != nil {
		t.Fatalf("write response: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected readReverseConnectResponse to fail for 403 response")
		}
		var rejectionErr *reverseConnectRejectionError
		if !errors.As(err, &rejectionErr) {
			t.Fatalf("expected reverseConnectRejectionError, got: %T %v", err, err)
		}
		if rejectionErr.statusCode != http.StatusForbidden {
			t.Fatalf("rejection statusCode=%d, want %d", rejectionErr.statusCode, http.StatusForbidden)
		}
		if rejectionErr.code != "ip_banned" {
			t.Fatalf("rejection code=%q, want %q", rejectionErr.code, "ip_banned")
		}
		if rejectionErr.detail != "ip is banned (code=ip_banned)" {
			t.Fatalf("rejection detail=%q, want %q", rejectionErr.detail, "ip is banned (code=ip_banned)")
		}
		if !rejectionErr.IsFatal() {
			t.Fatalf("expected ip_banned rejection to be fatal: %+v", rejectionErr)
		}
		if strings.Contains(err.Error(), `{\"ok\":false`) {
			t.Fatalf("expected formatted error detail instead of raw JSON: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for readReverseConnectResponse error")
	}
}

func TestReverseConnectRejectionErrorIsFatal(t *testing.T) {
	t.Parallel()

	var nilErr *reverseConnectRejectionError
	if nilErr.IsFatal() {
		t.Fatal("nil rejection error must not be fatal")
	}

	tests := []struct {
		err  *reverseConnectRejectionError
		name string
		want bool
	}{
		{
			name: "fatal by known code",
			err: &reverseConnectRejectionError{
				statusCode: http.StatusTooManyRequests,
				code:       "ip_banned",
			},
			want: true,
		},
		{
			name: "fatal by status code",
			err: &reverseConnectRejectionError{
				statusCode: http.StatusUpgradeRequired,
			},
			want: true,
		},
		{
			name: "transient retry status",
			err: &reverseConnectRejectionError{
				statusCode: http.StatusServiceUnavailable,
			},
			want: false,
		},
		{
			name: "unknown code and status",
			err: &reverseConnectRejectionError{
				statusCode: http.StatusInternalServerError,
				code:       "unexpected_failure",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.err.IsFatal(); got != tt.want {
				t.Fatalf("IsFatal()=%t, want %t for %+v", got, tt.want, tt.err)
			}
		})
	}
}

func TestWaitForReverseStart_HTTPMode(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, types.TLSStartMarker)
	}()

	_, err := peer.Write([]byte{types.TLSStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReverseStart failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_TLSMode(t *testing.T) {
	t.Parallel()

	l := &Listener{
		stopCh:    make(chan struct{}),
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, types.TLSStartMarker)
	}()

	_, err := peer.Write([]byte{types.TLSStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReverseStart failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_IgnoresKeepaliveMarker(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, types.TLSStartMarker)
	}()

	_, err := peer.Write([]byte{types.ReverseKeepaliveMarker})
	if err != nil {
		t.Fatalf("write keepalive marker: %v", err)
	}
	_, err = peer.Write([]byte{types.TLSStartMarker})
	if err != nil {
		t.Fatalf("write start marker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReverseStart failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_TLSRejectsHTTPMarker(t *testing.T) {
	t.Parallel()

	l := &Listener{
		stopCh:    make(chan struct{}),
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, types.TLSStartMarker)
	}()

	_, err := peer.Write([]byte{types.NonTLSStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected invalid marker error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_HTTPRejectsTLSMarker(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, types.NonTLSStartMarker)
	}()

	_, err := peer.Write([]byte{types.TLSStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected invalid marker error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_StopCancelsWait(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, types.TLSStartMarker)
	}()

	close(l.stopCh)
	_ = peer.Close()

	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("expected net.ErrClosed when listener stops, got: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitForReverseStart did not stop after cancellation")
	}
}
