package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

type fakeStream struct{}

func (s *fakeStream) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (s *fakeStream) Write(p []byte) (int, error) {
	return len(p), nil
}

func (s *fakeStream) Close() error {
	return nil
}

func (s *fakeStream) SetDeadline(time.Time) error {
	return nil
}

func (s *fakeStream) SetReadDeadline(time.Time) error {
	return nil
}

func (s *fakeStream) SetWriteDeadline(time.Time) error {
	return nil
}

type fakeSession struct {
	openStream   portal.Stream
	openErr      error
	acceptStream portal.Stream
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

func (s *fakeSession) Close() error {
	return nil
}

func assertCORSHeaders(t *testing.T, headers http.Header) {
	t.Helper()

	require.Equal(t, "*", headers.Get("Access-Control-Allow-Origin"), "Access-Control-Allow-Origin")
	require.Equal(t, "GET, OPTIONS", headers.Get("Access-Control-Allow-Methods"), "Access-Control-Allow-Methods")
	require.Equal(t, "Content-Type, Accept, Accept-Encoding", headers.Get("Access-Control-Allow-Headers"), "Access-Control-Allow-Headers")
}

func TestWithCORSMiddleware_OPTIONSShortCircuitsInnerHandler(t *testing.T) {
	t.Parallel()

	innerCalled := false
	handler := withCORSMiddleware(func(http.ResponseWriter, *http.Request) {
		innerCalled = true
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/app", http.NoBody)
	handler(rec, req)

	require.False(t, innerCalled, "expected OPTIONS request to bypass inner handler")
	require.Equal(t, http.StatusOK, rec.Code, "status")
	assertCORSHeaders(t, rec.Header())
}

func TestWithCORSMiddleware_GETSetsHeadersAndCallsInnerHandler(t *testing.T) {
	t.Parallel()

	innerCalled := false
	handler := withCORSMiddleware(func(w http.ResponseWriter, _ *http.Request) {
		innerCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app", http.NoBody)
	handler(rec, req)

	require.True(t, innerCalled, "expected GET request to call inner handler")
	require.Equal(t, http.StatusNoContent, rec.Code, "status")
	assertCORSHeaders(t, rec.Header())
}

func TestServeWebTransportInvalidAddrTriggersShutdownAndCleanup(t *testing.T) {
	cert, _, err := utils.GenerateSelfSignedCert()
	require.NoError(t, err, "generateSelfSignedCert()")

	shutdownCalled := make(chan struct{}, 1)
	cleanup := serveWebTransport(":-1", newTestRelayServer(t), nil, &cert, func() {
		select {
		case shutdownCalled <- struct{}{}:
		default:
		}
	})

	select {
	case <-shutdownCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown callback was not invoked within timeout")
	}

	cleanupDone := make(chan struct{})
	cleanupPanic := make(chan any, 1)
	go func() {
		defer close(cleanupDone)
		defer func() {
			if r := recover(); r != nil {
				cleanupPanic <- r
			}
		}()
		cleanup()
	}()

	select {
	case p := <-cleanupPanic:
		t.Fatalf("cleanup function panicked: %v", p)
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup function did not return within timeout")
	}
}

func TestWrapRelayStream_NilAndEmptyIPPassthrough(t *testing.T) {
	t.Parallel()

	baseStream := &fakeStream{}
	require.Equal(t, baseStream, wrapRelayStream(baseStream, ""), "expected empty IP to return original stream")
	require.Nil(t, wrapRelayStream(nil, "203.0.113.11"), "expected nil stream to remain nil")
}

func TestStreamClientIP_FromWrappedStream(t *testing.T) {
	t.Parallel()

	baseStream := &fakeStream{}
	wrapped := wrapRelayStream(baseStream, "203.0.113.12")
	require.NotEqual(t, baseStream, wrapped, "expected non-empty IP to wrap stream")
	require.Equal(t, "203.0.113.12", streamClientIP(wrapped), "streamClientIP(wrapped)")
	require.Equal(t, "", streamClientIP(baseStream), "streamClientIP(unwrapped)")
}

func TestWrapRelaySession_EmptyIPPassthrough(t *testing.T) {
	t.Parallel()

	baseSession := &fakeSession{}
	require.Equal(t, baseSession, wrapRelaySession(baseSession, ""), "expected empty IP to return original session")
}

func TestWrapRelaySession_WithIPWrapsOpenAndAcceptStreams(t *testing.T) {
	t.Parallel()

	openBase := &fakeStream{}
	acceptBase := &fakeStream{}
	baseSession := &fakeSession{
		openStream:   openBase,
		acceptStream: acceptBase,
	}

	wrappedSession := wrapRelaySession(baseSession, "203.0.113.13")

	openStream, err := wrappedSession.OpenStream(context.Background())
	require.NoError(t, err, "OpenStream()")
	require.NotEqual(t, openBase, openStream, "expected OpenStream result to be wrapped")
	require.Equal(t, "203.0.113.13", streamClientIP(openStream), "streamClientIP(OpenStream())")

	acceptStream, err := wrappedSession.AcceptStream(context.Background())
	require.NoError(t, err, "AcceptStream()")
	require.NotEqual(t, acceptBase, acceptStream, "expected AcceptStream result to be wrapped")
	require.Equal(t, "203.0.113.13", streamClientIP(acceptStream), "streamClientIP(AcceptStream())")

	require.Equal(t, 1, baseSession.openCalls, "OpenStream called")
	require.Equal(t, 1, baseSession.acceptCalls, "AcceptStream called")
}

func TestWrapRelaySession_OpenStreamErrorPropagation(t *testing.T) {
	t.Parallel()

	openErr := errors.New("open failed")
	baseSession := &fakeSession{openErr: openErr}
	wrappedSession := wrapRelaySession(baseSession, "203.0.113.14")

	gotStream, err := wrappedSession.OpenStream(context.Background())
	require.ErrorIs(t, err, openErr, "OpenStream()")
	require.Nil(t, gotStream, "expected nil stream when OpenStream fails")
}

func TestWrapRelaySession_AcceptStreamErrorPropagation(t *testing.T) {
	t.Parallel()

	acceptErr := errors.New("accept failed")
	baseSession := &fakeSession{acceptErr: acceptErr}
	wrappedSession := wrapRelaySession(baseSession, "203.0.113.15")

	gotStream, err := wrappedSession.AcceptStream(context.Background())
	require.ErrorIs(t, err, acceptErr, "AcceptStream()")
	require.Nil(t, gotStream, "expected nil stream when AcceptStream fails")
}
