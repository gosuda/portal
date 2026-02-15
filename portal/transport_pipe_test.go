package portal

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestPipeSessionPair(t *testing.T) {
	client, server := NewPipeSessionPair()
	if client == nil || server == nil {
		t.Fatal("NewPipeSessionPair returned nil")
	}
	if client.peer != server {
		t.Error("client peer not set to server")
	}
	if server.peer != client {
		t.Error("server peer not set to client")
	}
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

		if clientErr != nil {
			t.Fatalf("client.OpenStream: %v", clientErr)
		}
		if serverErr != nil {
			t.Fatalf("server.AcceptStream: %v", serverErr)
		}

		// Test data transfer
		msg := []byte("hello from client")
		if _, err := clientStream.Write(msg); err != nil {
			t.Fatalf("clientStream.Write: %v", err)
		}

		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(serverStream, buf); err != nil {
			t.Fatalf("serverStream.Read: %v", err)
		}

		if string(buf) != string(msg) {
			t.Errorf("got %q, want %q", buf, msg)
		}

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

		if serverErr != nil {
			t.Fatalf("server.OpenStream: %v", serverErr)
		}
		if clientErr != nil {
			t.Fatalf("client.AcceptStream: %v", clientErr)
		}

		// Test data transfer
		msg := []byte("hello from server")
		if _, err := serverStream.Write(msg); err != nil {
			t.Fatalf("serverStream.Write: %v", err)
		}

		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(clientStream, buf); err != nil {
			t.Fatalf("clientStream.Read: %v", err)
		}

		if string(buf) != string(msg) {
			t.Errorf("got %q, want %q", buf, msg)
		}

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
	if err != nil {
		t.Fatalf("server.AcceptStream: %v", err)
	}
	clientStream := <-streamC

	defer clientStream.Close()
	defer serverStream.Close()

	// Bidirectional transfer
	msg1 := []byte("ping")
	msg2 := []byte("pong")

	// Client -> Server
	if _, err := clientStream.Write(msg1); err != nil {
		t.Fatalf("Write ping: %v", err)
	}

	buf1 := make([]byte, len(msg1))
	if _, err := io.ReadFull(serverStream, buf1); err != nil {
		t.Fatalf("Read ping: %v", err)
	}
	if string(buf1) != string(msg1) {
		t.Errorf("got %q, want %q", buf1, msg1)
	}

	// Server -> Client
	if _, err := serverStream.Write(msg2); err != nil {
		t.Fatalf("Write pong: %v", err)
	}

	buf2 := make([]byte, len(msg2))
	if _, err := io.ReadFull(clientStream, buf2); err != nil {
		t.Fatalf("Read pong: %v", err)
	}
	if string(buf2) != string(msg2) {
		t.Errorf("got %q, want %q", buf2, msg2)
	}
}

func TestPipeSessionClose(t *testing.T) {
	client, server := NewPipeSessionPair()

	ctx := context.Background()

	// Close client
	if err := client.Close(); err != nil {
		t.Errorf("client.Close: %v", err)
	}

	// Subsequent OpenStream should fail
	if _, err := client.OpenStream(ctx); err == nil {
		t.Error("OpenStream on closed session should fail")
	}

	// Subsequent AcceptStream should fail
	if _, err := client.AcceptStream(ctx); err == nil {
		t.Error("AcceptStream on closed session should fail")
	}

	// Double close should be idempotent (no error)
	if err := client.Close(); err != nil {
		t.Errorf("double Close should be idempotent, got error: %v", err)
	}

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
	if err == nil {
		t.Fatal("AcceptStream should fail on context timeout")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
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
	if err == nil {
		t.Fatal("OpenStream should fail on canceled context")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
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
	for i := 0; i < numStreams; i++ {
		i := i
		go func() {
			defer wg.Done()
			stream, err := client.OpenStream(ctx)
			if err != nil {
				t.Errorf("OpenStream %d: %v", i, err)
				return
			}
			defer stream.Close()

			msg := []byte{byte(i)}
			if _, err := stream.Write(msg); err != nil {
				t.Errorf("Write %d: %v", i, err)
			}
		}()

		go func() {
			defer wg.Done()
			stream, err := server.AcceptStream(ctx)
			if err != nil {
				t.Errorf("AcceptStream %d: %v", i, err)
				return
			}
			defer stream.Close()

			buf := make([]byte, 1)
			if _, err := io.ReadFull(stream, buf); err != nil {
				t.Errorf("Read %d: %v", i, err)
			}
		}()
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
		s, _ := client.OpenStream(ctx)
		streamC <- s
	}()

	serverStream, err := server.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	clientStream := <-streamC

	defer clientStream.Close()
	defer serverStream.Close()

	buf := make([]byte, 1)

	// Test SetDeadline (must run before write loop fills the channel)
	if err := clientStream.SetDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Errorf("SetDeadline: %v", err)
	}

	_, err = clientStream.Read(buf)
	if err == nil {
		t.Error("Read should timeout after SetDeadline")
	}

	// Reset deadline
	clientStream.SetDeadline(time.Time{})

	// Test SetReadDeadline
	if err := serverStream.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}

	_, err = serverStream.Read(buf)
	if err == nil {
		t.Error("Read should timeout")
	}

	// Test SetWriteDeadline
	// Fill the pipe buffer first
	serverStream.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	data := make([]byte, 1024*1024) // 1MB should fill pipe buffer
	for {
		_, err := serverStream.Write(data)
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
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}

	stream2, err := server.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}

	// Write data
	msg := []byte("test")
	if _, err := stream1.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Close session (should close pending streams)
	if err := client.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// Reads/writes on closed streams should fail
	buf := make([]byte, len(msg))
	_, err = stream2.Read(buf)
	// Read may succeed if data was buffered, or fail if pipe closed
	// Either is acceptable behavior

	if _, err := stream1.Write(msg); err == nil {
		t.Error("Write on stream after session close should fail")
	}

	server.Close()
}
