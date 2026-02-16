package portal

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

func newRelayTestCredential(t *testing.T) *cryptoops.Credential {
	t.Helper()

	cred, err := cryptoops.NewCredential()
	if err != nil {
		t.Fatalf("cryptoops.NewCredential: %v", err)
	}

	return cred
}

type controlledSession struct {
	acceptStarted chan struct{}
	acceptRelease chan struct{}
	acceptCh      chan Stream
	closeCalled   chan struct{}

	acceptOnce sync.Once
	closeOnce  sync.Once
}

func newControlledSession() *controlledSession {
	return &controlledSession{
		acceptStarted: make(chan struct{}),
		acceptRelease: make(chan struct{}),
		acceptCh:      make(chan Stream, 1),
		closeCalled:   make(chan struct{}),
	}
}

func (s *controlledSession) OpenStream(context.Context) (Stream, error) {
	return nil, ErrPipeSessionClosed
}

func (s *controlledSession) AcceptStream(ctx context.Context) (Stream, error) {
	s.acceptOnce.Do(func() { close(s.acceptStarted) })

	select {
	case <-s.acceptRelease:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case stream := <-s.acceptCh:
		if stream == nil {
			return nil, ErrPipeSessionClosed
		}
		return stream, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *controlledSession) Close() error {
	s.closeOnce.Do(func() { close(s.closeCalled) })
	return nil
}

type trackingStream struct {
	readCalls  atomic.Int32
	closeCalls atomic.Int32
}

func (s *trackingStream) Read([]byte) (int, error) {
	s.readCalls.Add(1)
	return 0, io.EOF
}

func (s *trackingStream) Write(p []byte) (int, error) {
	return len(p), nil
}

func (s *trackingStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *trackingStream) SetDeadline(time.Time) error {
	return nil
}

func (s *trackingStream) SetReadDeadline(time.Time) error {
	return nil
}

func (s *trackingStream) SetWriteDeadline(time.Time) error {
	return nil
}

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestRelayServerHandleSessionRejectsAfterStop(t *testing.T) {
	server := NewRelayServer(newRelayTestCredential(t), []string{"localhost:8080"})
	server.Stop()

	_, serverSess := NewPipeSessionPair()
	server.HandleSession(serverSess)

	server.connectionsLock.RLock()
	connectionCount := len(server.connections)
	connIDCounter := server.connidCounter
	server.connectionsLock.RUnlock()

	if connectionCount != 0 {
		t.Fatalf("expected no registered connections after stop, got %d", connectionCount)
	}
	if connIDCounter != 0 {
		t.Fatalf("expected connidCounter to remain 0 after stop, got %d", connIDCounter)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := serverSess.AcceptStream(ctx)
	if !errors.Is(err, ErrPipeSessionClosed) {
		t.Fatalf("expected incoming session to be closed after stop, got %v", err)
	}
}

func TestRelayServerStopRejectsLateAcceptedStream(t *testing.T) {
	server := NewRelayServer(newRelayTestCredential(t), []string{"localhost:8080"})
	sess := newControlledSession()
	server.HandleSession(sess)

	waitForSignal(t, sess.acceptStarted, "AcceptStream entry")

	stopDone := make(chan struct{})
	go func() {
		server.Stop()
		close(stopDone)
	}()

	waitForSignal(t, sess.closeCalled, "session close during Stop")

	stream := &trackingStream{}
	sess.acceptCh <- stream
	close(sess.acceptRelease)

	waitForSignal(t, stopDone, "Stop completion")

	if got := stream.readCalls.Load(); got != 0 {
		t.Fatalf("expected no stream handler reads after stop, got %d", got)
	}
	if got := stream.closeCalls.Load(); got == 0 {
		t.Fatal("expected late-accepted stream to be closed during stop")
	}

	server.connectionsLock.RLock()
	connectionCount := len(server.connections)
	server.connectionsLock.RUnlock()
	if connectionCount != 0 {
		t.Fatalf("expected all connections cleaned up after stop, got %d", connectionCount)
	}
}

func TestRelayServerStopWaitsForRelayedConnectionWorker(t *testing.T) {
	server := NewRelayServer(newRelayTestCredential(t), []string{"localhost:8080"})
	server.Start()

	hostClientSession, hostServerSession := NewPipeSessionPair()
	server.HandleSession(hostServerSession)
	hostClient := NewRelayClient(hostClientSession)
	defer hostClient.Close()

	hostCred := newRelayTestCredential(t)
	err := hostClient.RegisterLease(hostCred, &rdverb.Lease{
		Name: "stop-waits-for-relay-worker",
		Alpn: []string{"test-proto"},
	})
	if err != nil {
		t.Fatalf("RegisterLease: %v", err)
	}

	peerClientSession, peerServerSession := NewPipeSessionPair()
	server.HandleSession(peerServerSession)
	peerClient := NewRelayClient(peerClientSession)
	defer peerClient.Close()

	relayStarted := make(chan struct{})
	releaseRelay := make(chan struct{})
	var relayStartedOnce sync.Once
	server.SetEstablishRelayCallback(func(clientStream, leaseStream Stream, _ string) {
		relayStartedOnce.Do(func() { close(relayStarted) })
		<-releaseRelay
		closeWithLog(clientStream, "[RelayServer] Failed to close client stream in relay worker test callback")
		closeWithLog(leaseStream, "[RelayServer] Failed to close lease stream in relay worker test callback")
	})

	peerCred := newRelayTestCredential(t)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _, _ = peerClient.RequestConnection(hostCred.ID(), "test-proto", peerCred)
	}()

	waitForSignal(t, relayStarted, "relay worker start")

	stopDone := make(chan struct{})
	go func() {
		server.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("expected Stop to wait for relayed connection worker")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseRelay)

	waitForSignal(t, stopDone, "Stop completion with relayed worker")
	waitForSignal(t, requestDone, "request completion after relayed worker release")
}
