package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gosuda.org/portal/portal"
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

	if got := headers.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	if got := headers.Get("Access-Control-Allow-Methods"); got != "GET, OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, "GET, OPTIONS")
	}
	if got := headers.Get("Access-Control-Allow-Headers"); got != "Content-Type, Accept, Accept-Encoding" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type, Accept, Accept-Encoding")
	}
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

	if innerCalled {
		t.Fatal("expected OPTIONS request to bypass inner handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
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

	if !innerCalled {
		t.Fatal("expected GET request to call inner handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	assertCORSHeaders(t, rec.Header())
}

func TestWrapRelayStream_NilAndEmptyIPPassthrough(t *testing.T) {
	t.Parallel()

	baseStream := &fakeStream{}
	if got := wrapRelayStream(baseStream, ""); got != baseStream {
		t.Fatal("expected empty IP to return original stream")
	}
	if got := wrapRelayStream(nil, "203.0.113.11"); got != nil {
		t.Fatal("expected nil stream to remain nil")
	}
}

func TestStreamClientIP_FromWrappedStream(t *testing.T) {
	t.Parallel()

	baseStream := &fakeStream{}
	wrapped := wrapRelayStream(baseStream, "203.0.113.12")
	if wrapped == baseStream {
		t.Fatal("expected non-empty IP to wrap stream")
	}
	if got := streamClientIP(wrapped); got != "203.0.113.12" {
		t.Fatalf("streamClientIP(wrapped) = %q, want %q", got, "203.0.113.12")
	}
	if got := streamClientIP(baseStream); got != "" {
		t.Fatalf("streamClientIP(unwrapped) = %q, want empty", got)
	}
}

func TestWrapRelaySession_EmptyIPPassthrough(t *testing.T) {
	t.Parallel()

	baseSession := &fakeSession{}
	if got := wrapRelaySession(baseSession, ""); got != baseSession {
		t.Fatal("expected empty IP to return original session")
	}
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
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if openStream == openBase {
		t.Fatal("expected OpenStream result to be wrapped")
	}
	if got := streamClientIP(openStream); got != "203.0.113.13" {
		t.Fatalf("streamClientIP(OpenStream()) = %q, want %q", got, "203.0.113.13")
	}

	acceptStream, err := wrappedSession.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("AcceptStream() error = %v", err)
	}
	if acceptStream == acceptBase {
		t.Fatal("expected AcceptStream result to be wrapped")
	}
	if got := streamClientIP(acceptStream); got != "203.0.113.13" {
		t.Fatalf("streamClientIP(AcceptStream()) = %q, want %q", got, "203.0.113.13")
	}

	if baseSession.openCalls != 1 {
		t.Fatalf("OpenStream called %d times, want 1", baseSession.openCalls)
	}
	if baseSession.acceptCalls != 1 {
		t.Fatalf("AcceptStream called %d times, want 1", baseSession.acceptCalls)
	}
}

func TestWrapRelaySession_OpenStreamErrorPropagation(t *testing.T) {
	t.Parallel()

	openErr := errors.New("open failed")
	baseSession := &fakeSession{openErr: openErr}
	wrappedSession := wrapRelaySession(baseSession, "203.0.113.14")

	gotStream, err := wrappedSession.OpenStream(context.Background())
	if !errors.Is(err, openErr) {
		t.Fatalf("OpenStream() error = %v, want %v", err, openErr)
	}
	if gotStream != nil {
		t.Fatal("expected nil stream when OpenStream fails")
	}
}

func TestWrapRelaySession_AcceptStreamErrorPropagation(t *testing.T) {
	t.Parallel()

	acceptErr := errors.New("accept failed")
	baseSession := &fakeSession{acceptErr: acceptErr}
	wrappedSession := wrapRelaySession(baseSession, "203.0.113.15")

	gotStream, err := wrappedSession.AcceptStream(context.Background())
	if !errors.Is(err, acceptErr) {
		t.Fatalf("AcceptStream() error = %v, want %v", err, acceptErr)
	}
	if gotStream != nil {
		t.Fatal("expected nil stream when AcceptStream fails")
	}
}
