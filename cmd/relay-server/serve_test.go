package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

// fakeStream implements portal.Stream for testing
type fakeStream struct{}

func (s *fakeStream) Read([]byte) (int, error)         { return 0, io.EOF }
func (s *fakeStream) Write(p []byte) (int, error)      { return len(p), nil }
func (s *fakeStream) Close() error                     { return nil }
func (s *fakeStream) SetDeadline(time.Time) error      { return nil }
func (s *fakeStream) SetReadDeadline(time.Time) error  { return nil }
func (s *fakeStream) SetWriteDeadline(time.Time) error { return nil }

// fakeSession implements portal.Session for testing
type fakeSession struct {
	openStream   portal.Stream
	acceptStream portal.Stream
	openErr      error
	acceptErr    error
	openCalls    int
	acceptCalls  int
}

func (s *fakeSession) OpenStream(context.Context) (portal.Stream, error) {
	s.openCalls++
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.openStream, nil
}

func (s *fakeSession) AcceptStream(context.Context) (portal.Stream, error) {
	s.acceptCalls++
	if s.acceptErr != nil {
		return nil, s.acceptErr
	}
	return s.acceptStream, nil
}

func (s *fakeSession) Close() error { return nil }

// Test Harness
type serveHTTPTestHarness struct {
	srv           *http.Server
	shutdownCalls *atomic.Int32
}

func newServeHTTPTestHarness(t *testing.T, noIndex bool, certHash []byte) *serveHTTPTestHarness {
	t.Helper()

	frontend := newTestFrontendWithDistFS(fstest.MapFS{
		"dist/app/portal.html": {Data: []byte(`<html><body>portal</body></html>`)},
	})

	serv := newTestRelayServer(t)
	shutdownCalls := &atomic.Int32{}
	srv := serveHTTP(":0", serv, nil, frontend, noIndex, certHash,
		"https://*.portal.example.com", "https://portal.example.com",
		func() { shutdownCalls.Add(1) })

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	})

	return &serveHTTPTestHarness{srv: srv, shutdownCalls: shutdownCalls}
}

func (h *serveHTTPTestHarness) serve(host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, http.NoBody)
	req.Host = host
	rec := httptest.NewRecorder()
	h.srv.Handler.ServeHTTP(rec, req)
	return rec
}

// CORS Middleware Tests
func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	t.Run("OPTIONS bypasses handler", func(t *testing.T) {
		t.Parallel()
		innerCalled := false
		handler := withCORSMiddleware(func(http.ResponseWriter, *http.Request) { innerCalled = true })

		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodOptions, "/app", http.NoBody))

		require.False(t, innerCalled)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("GET calls handler with CORS headers", func(t *testing.T) {
		t.Parallel()
		innerCalled := false
		handler := withCORSMiddleware(func(w http.ResponseWriter, _ *http.Request) {
			innerCalled = true
			w.WriteHeader(http.StatusNoContent)
		})

		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/app", http.NoBody))

		require.True(t, innerCalled)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	})
}

// HTTP Routes Tests
func TestHTTPRoutes(t *testing.T) {
	t.Parallel()

	t.Run("robots.txt with noindex", func(t *testing.T) {
		t.Parallel()
		h := newServeHTTPTestHarness(t, true, nil)
		rec := h.serve("portal.example.com", "/robots.txt")

		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "Disallow: /")
	})

	t.Run("healthz", func(t *testing.T) {
		t.Parallel()
		h := newServeHTTPTestHarness(t, false, nil)
		rec := h.serve("portal.example.com", "/healthz")

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, `{"status":"ok"}`, rec.Body.String())
	})

	t.Run("relay returns upgrade required", func(t *testing.T) {
		t.Parallel()
		h := newServeHTTPTestHarness(t, false, nil)
		rec := h.serve("portal.example.com", "/relay")

		require.Equal(t, http.StatusUpgradeRequired, rec.Code)
		require.Contains(t, rec.Body.String(), "WebTransport (HTTP/3) required")
	})

	t.Run("cert-hash when configured", func(t *testing.T) {
		t.Parallel()
		h := newServeHTTPTestHarness(t, false, []byte{0x01, 0xab})
		rec := h.serve("portal.example.com", "/cert-hash")

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, `{"algorithm":"sha-256","hash":"01ab"}`, rec.Body.String())
	})
}

// WebTransport Tests
func TestWebTransportRelay(t *testing.T) {
	t.Run("bans using forwarded IP", func(t *testing.T) {
		admin := NewAdmin(0, nil, nil, "", "")
		admin.GetIPManager().BanIP("198.51.100.9")

		req := httptest.NewRequest(http.MethodConnect, "https://relay.example/relay", http.NoBody)
		req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.1")

		rec := httptest.NewRecorder()
		upgradeCalled, handleCalled := false, false

		handleWebTransportRelayRequest(rec, req, admin,
			func(http.ResponseWriter, *http.Request) (*webtransport.Session, error) {
				upgradeCalled = true
				return nil, nil
			},
			func(portal.Session) { handleCalled = true },
		)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, upgradeCalled)
		assert.False(t, handleCalled)
	})

	t.Run("failed upgrade does not pollute association", func(t *testing.T) {
		admin := NewAdmin(0, nil, nil, "", "")
		req := httptest.NewRequest(http.MethodConnect, "https://relay.example/relay", http.NoBody)
		req.Header.Set("X-Forwarded-For", "198.51.100.22")

		rec := httptest.NewRecorder()
		handleCalled := false

		handleWebTransportRelayRequest(rec, req, admin,
			func(http.ResponseWriter, *http.Request) (*webtransport.Session, error) {
				return nil, errors.New("upgrade failed")
			},
			func(portal.Session) { handleCalled = true },
		)

		assert.False(t, handleCalled)
		assert.Empty(t, admin.GetIPManager().PopPendingIP())
	})

	t.Run("invalid addr triggers shutdown", func(t *testing.T) {
		cert, _, err := utils.GenerateSelfSignedCert()
		require.NoError(t, err)

		shutdownCalled := make(chan struct{}, 1)
		cleanup := serveWebTransport(":-1", newTestRelayServer(t), nil, &cert, func() {
			shutdownCalled <- struct{}{}
		})

		select {
		case <-shutdownCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("shutdown timeout")
		}

		done := make(chan struct{})
		go func() { cleanup(); close(done) }()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("cleanup timeout")
		}
	})
}

// Stream/Session Wrapper Tests
func TestStreamSessionWrappers(t *testing.T) {
	t.Parallel()

	t.Run("wrap stream with IP", func(t *testing.T) {
		t.Parallel()
		base := &fakeStream{}
		wrapped := wrapRelayStream(base, "203.0.113.12")

		require.NotEqual(t, base, wrapped)
		require.Equal(t, "203.0.113.12", streamClientIP(wrapped))
		require.Equal(t, "", streamClientIP(base))
	})

	t.Run("wrap stream passthrough", func(t *testing.T) {
		t.Parallel()
		base := &fakeStream{}
		require.Equal(t, base, wrapRelayStream(base, ""))
		require.Nil(t, wrapRelayStream(nil, "203.0.113.11"))
	})

	t.Run("wrap session with IP", func(t *testing.T) {
		t.Parallel()
		openBase, acceptBase := &fakeStream{}, &fakeStream{}
		base := &fakeSession{openStream: openBase, acceptStream: acceptBase}

		wrapped := wrapRelaySession(base, "203.0.113.13")

		openStream, err := wrapped.OpenStream(context.Background())
		require.NoError(t, err)
		require.Equal(t, "203.0.113.13", streamClientIP(openStream))

		acceptStream, err := wrapped.AcceptStream(context.Background())
		require.NoError(t, err)
		require.Equal(t, "203.0.113.13", streamClientIP(acceptStream))

		require.Equal(t, 1, base.openCalls)
		require.Equal(t, 1, base.acceptCalls)
	})

	t.Run("wrap session error propagation", func(t *testing.T) {
		t.Parallel()
		openErr := errors.New("open failed")
		base := &fakeSession{openErr: openErr}
		wrapped := wrapRelaySession(base, "203.0.113.14")

		stream, err := wrapped.OpenStream(context.Background())
		require.ErrorIs(t, err, openErr)
		require.Nil(t, stream)
	})
}
