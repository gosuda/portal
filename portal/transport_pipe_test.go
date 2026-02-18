package portal

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipeSessionPair(t *testing.T) {
	client, server := NewPipeSessionPair()
	require.NotNil(t, client, "NewPipeSessionPair returned nil client")
	require.NotNil(t, server, "NewPipeSessionPair returned nil server")
	assert.Equal(t, server, client.peer, "client peer not set to server")
	assert.Equal(t, client, server.peer, "server peer not set to client")
}

func TestPipeSessionBidirectionalStreams(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()
	defer server.Close()

	ctx := context.Background()

	// Client opens stream
	t.Run("ClientToServer", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(2)

		var clientStream, serverStream Stream
		var clientErr, serverErr error

		go func() {
			defer wg.Done()
			clientStream, clientErr = client.OpenStream(ctx)
		}()

		go func() {
			defer wg.Done()
			serverStream, serverErr = server.AcceptStream(ctx)
		}()

		wg.Wait()

		require.NoError(t, clientErr, "client.OpenStream")
		require.NoError(t, serverErr, "server.AcceptStream")

		// Test data transfer
		msg := []byte("hello from client")
		_, err := clientStream.Write(msg)
		require.NoError(t, err, "clientStream.Write")

		buf := make([]byte, len(msg))
		_, err = io.ReadFull(serverStream, buf)
		require.NoError(t, err, "serverStream.Read")

		assert.Equal(t, msg, buf, "data mismatch")

		clientStream.Close()
		serverStream.Close()
	})

	// Server opens stream
	t.Run("ServerToClient", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(2)

		var clientStream, serverStream Stream
		var clientErr, serverErr error

		go func() {
			defer wg.Done()
			serverStream, serverErr = server.OpenStream(ctx)
		}()

		go func() {
			defer wg.Done()
			clientStream, clientErr = client.AcceptStream(ctx)
		}()

		wg.Wait()

		require.NoError(t, serverErr, "server.OpenStream")
		require.NoError(t, clientErr, "client.AcceptStream")

		// Test data transfer
		msg := []byte("hello from server")
		_, err := serverStream.Write(msg)
		require.NoError(t, err, "serverStream.Write")

		buf := make([]byte, len(msg))
		_, err = io.ReadFull(clientStream, buf)
		require.NoError(t, err, "clientStream.Read")

		assert.Equal(t, msg, buf, "data mismatch")

		serverStream.Close()
		clientStream.Close()
	})
}

func TestPipeSessionDataTransfer(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()
	defer server.Close()

	ctx := context.Background()

	// Open stream
	streamC := make(chan Stream, 1)
	go func() {
		s, err := client.OpenStream(ctx)
		if err != nil {
			t.Errorf("client.OpenStream: %v", err)
			return
		}
		streamC <- s
	}()

	serverStream, err := server.AcceptStream(ctx)
	require.NoError(t, err, "server.AcceptStream")
	clientStream := <-streamC

	defer clientStream.Close()
	defer serverStream.Close()

	// Bidirectional transfer
	msg1 := []byte("ping")
	msg2 := []byte("pong")

	// Client -> Server
	_, err = clientStream.Write(msg1)
	require.NoError(t, err, "Write ping")

	buf1 := make([]byte, len(msg1))
	_, err = io.ReadFull(serverStream, buf1)
	require.NoError(t, err, "Read ping")
	assert.Equal(t, msg1, buf1)

	// Server -> Client
	_, err = serverStream.Write(msg2)
	require.NoError(t, err, "Write pong")

	buf2 := make([]byte, len(msg2))
	_, err = io.ReadFull(clientStream, buf2)
	require.NoError(t, err, "Read pong")
	assert.Equal(t, msg2, buf2)
}

func TestPipeSessionClose(t *testing.T) {
	client, server := NewPipeSessionPair()

	ctx := context.Background()

	// Close client
	assert.NoError(t, client.Close(), "client.Close")

	// Subsequent OpenStream should fail
	_, err := client.OpenStream(ctx)
	assert.Error(t, err, "OpenStream on closed session should fail")

	// Subsequent AcceptStream should fail
	_, err = client.AcceptStream(ctx)
	assert.Error(t, err, "AcceptStream on closed session should fail")

	// Double close should be idempotent (no error)
	assert.NoError(t, client.Close(), "double Close should be idempotent")

	server.Close()
}

func TestPipeSessionAcceptContextCancel(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// AcceptStream should return context error when canceled
	_, err := server.AcceptStream(ctx)
	assert.Error(t, err, "AcceptStream should fail on context timeout")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestPipeSessionAcceptUnblocksOnClose(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()

	errCh := make(chan error, 1)
	started := make(chan struct{})

	go func() {
		close(started)
		_, err := server.AcceptStream(context.Background())
		errCh <- err
	}()

	<-started
	select {
	case err := <-errCh:
		t.Fatalf("AcceptStream returned before close: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, server.Close(), "server.Close")

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, ErrPipeSessionClosed)
	case <-time.After(time.Second):
		t.Fatal("AcceptStream did not unblock after close")
	}
}

func TestPipeSessionOpenContextCancel(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// OpenStream should fail on canceled context
	_, err := client.OpenStream(ctx)
	assert.Error(t, err, "OpenStream should fail on canceled context")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPipeSessionConcurrentOpenCloseRace(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()

	const openers = 64

	type openResult struct {
		stream     Stream
		err        error
		panicValue any
	}

	results := make(chan openResult, openers)
	start := make(chan struct{})

	for range openers {
		go func() {
			<-start
			defer func() {
				if r := recover(); r != nil {
					results <- openResult{panicValue: r}
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			stream, err := client.OpenStream(ctx)
			results <- openResult{stream: stream, err: err}
		}()
	}

	close(start)
	require.NoError(t, server.Close(), "server.Close")

	for range openers {
		select {
		case res := <-results:
			assert.Nil(t, res.panicValue, "OpenStream panicked during close race")
			assert.False(t, res.stream == nil && res.err == nil, "OpenStream returned nil stream and nil error")
			if res.stream != nil {
				_ = res.stream.Close()
			}
			if res.err != nil {
				assert.ErrorIs(t, res.err, ErrPipeSessionClosed, "unexpected OpenStream error during close race")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for OpenStream result")
		}
	}
}

func TestPipeSessionMultipleStreams(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()
	defer server.Close()

	ctx := context.Background()
	const numStreams = 10

	var wg sync.WaitGroup
	wg.Add(numStreams * 2)

	// Open multiple streams concurrently
	for i := range numStreams {
		go func(idx int) {
			defer wg.Done()
			stream, err := client.OpenStream(ctx)
			if err != nil {
				t.Errorf("OpenStream %d: %v", idx, err)
				return
			}
			defer stream.Close()

			msg := []byte{byte(idx)}
			if _, writeErr := stream.Write(msg); writeErr != nil {
				t.Errorf("Write %d: %v", idx, writeErr)
			}
		}(i)

		go func(idx int) {
			defer wg.Done()
			stream, err := server.AcceptStream(ctx)
			if err != nil {
				t.Errorf("AcceptStream %d: %v", idx, err)
				return
			}
			defer stream.Close()

			buf := make([]byte, 1)
			if _, readErr := io.ReadFull(stream, buf); readErr != nil {
				t.Errorf("Read %d: %v", idx, readErr)
			}
		}(i)
	}

	wg.Wait()
}

func TestPipeStreamDeadlines(t *testing.T) {
	client, server := NewPipeSessionPair()
	defer client.Close()
	defer server.Close()

	ctx := context.Background()

	// Open stream
	streamC := make(chan Stream, 1)
	go func() {
		s, err := client.OpenStream(ctx)
		if err != nil {
			t.Errorf("OpenStream: %v", err)
			streamC <- nil
			return
		}
		streamC <- s
	}()

	serverStream, err := server.AcceptStream(ctx)
	require.NoError(t, err, "AcceptStream")
	clientStream := <-streamC
	require.NotNil(t, clientStream, "clientStream is nil")

	defer clientStream.Close()
	defer serverStream.Close()

	buf := make([]byte, 1)

	// Test SetDeadline (must run before write loop fills the channel)
	setErr := clientStream.SetDeadline(time.Now().Add(10 * time.Millisecond))
	assert.NoError(t, setErr, "SetDeadline")

	_, err = clientStream.Read(buf)
	assert.Error(t, err, "Read should timeout after SetDeadline")

	// Reset deadline
	clientStream.SetDeadline(time.Time{})

	// Test SetReadDeadline
	setErr = serverStream.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	assert.NoError(t, setErr, "SetReadDeadline")

	_, err = serverStream.Read(buf)
	assert.Error(t, err, "Read should timeout")

	// Test SetWriteDeadline
	// Fill the pipe buffer first
	serverStream.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	data := make([]byte, 1024*1024) // 1MB should fill pipe buffer
	for {
		_, err = serverStream.Write(data)
		if err != nil {
			break // Expected timeout or pipe full
		}
	}
}

func TestPipeSessionCloseWithPendingStreams(t *testing.T) {
	client, server := NewPipeSessionPair()

	ctx := context.Background()

	// Open some streams
	stream1, err := client.OpenStream(ctx)
	require.NoError(t, err, "OpenStream")

	stream2, err := server.AcceptStream(ctx)
	require.NoError(t, err, "AcceptStream")

	// Write data
	msg := []byte("test")
	_, err = stream1.Write(msg)
	require.NoError(t, err, "Write")

	// Close session (should close pending streams)
	assert.NoError(t, client.Close(), "Close")

	// Reads/writes on closed streams should fail
	buf := make([]byte, len(msg))
	_, _ = stream2.Read(buf)
	// Read may succeed if data was buffered, or fail if pipe closed
	// Either is acceptable behavior

	_, writeErr := stream1.Write(msg)
	assert.Error(t, writeErr, "Write on stream after session close should fail")

	server.Close()
}
