package wsstream

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// mockWebSocketConn is a mock implementation of websocket.Conn for testing
type mockWebSocketConn struct {
	mu              sync.Mutex
	readData        [][]byte
	readIndex       int
	writeData       [][]byte
	closeCalled     bool
	nextReaderErr   error
	writeMessageErr error
	closeErr        error
}

func newMockConn(data []byte) *mockWebSocketConn {
	return &mockWebSocketConn{
		readData: [][]byte{data},
	}
}

func (m *mockWebSocketConn) NextReader() (messageType int, r io.Reader, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.nextReaderErr != nil {
		return 0, nil, m.nextReaderErr
	}

	if m.readIndex >= len(m.readData) {
		return 0, nil, io.EOF
	}

	data := m.readData[m.readIndex]
	m.readIndex++
	return websocket.BinaryMessage, bytes.NewReader(data), nil
}

func (m *mockWebSocketConn) WriteMessage(messageType int, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.writeMessageErr != nil {
		return m.writeMessageErr
	}

	// Copy data since caller may reuse the buffer
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	m.writeData = append(m.writeData, dataCopy)
	return nil
}

func (m *mockWebSocketConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalled = true
	return m.closeErr
}

// TestWsStream_Read tests the Read method
func TestWsStream_Read(t *testing.T) {
	t.Run("single message", func(t *testing.T) {
		data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		mock := newMockConn(data)
		stream := &WsStream{Conn: mock}

		buf := make([]byte, 10)
		n, err := stream.Read(buf)

		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if n != 5 {
			t.Errorf("Read() n = %v, want 5", n)
		}
		if !bytes.Equal(buf[:5], data) {
			t.Errorf("Read() data = %v, want %v", buf[:5], data)
		}
	})

	t.Run("multiple reads from same message", func(t *testing.T) {
		data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
		mock := newMockConn(data)
		stream := &WsStream{Conn: mock}

		buf1 := make([]byte, 3)
		n1, err := stream.Read(buf1)
		if err != nil {
			t.Fatalf("First Read() error = %v", err)
		}
		if n1 != 3 {
			t.Errorf("First Read() n = %v, want 3", n1)
		}

		buf2 := make([]byte, 10)
		n2, err := stream.Read(buf2)
		if err != nil {
			t.Fatalf("Second Read() error = %v", err)
		}
		if n2 != 5 {
			t.Errorf("Second Read() n = %v, want 5", n2)
		}
		if !bytes.Equal(buf2[:5], []byte{0x04, 0x05, 0x06, 0x07, 0x08}) {
			t.Errorf("Second Read() data = %v, want [4 5 6 7 8]", buf2[:5])
		}
	})

	t.Run("multiple messages", func(t *testing.T) {
		mock := &mockWebSocketConn{
			readData: [][]byte{
				{0x01, 0x02},
				{0x03, 0x04},
			},
		}
		stream := &WsStream{Conn: mock}

		buf := make([]byte, 10)
		n1, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("First Read() error = %v", err)
		}
		if n1 != 2 {
			t.Errorf("First Read() n = %v, want 2", n1)
		}

		n2, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("Second Read() error = %v", err)
		}
		if n2 != 2 {
			t.Errorf("Second Read() n = %v, want 2", n2)
		}
	})

	t.Run("empty buffer", func(t *testing.T) {
		mock := newMockConn([]byte{0x01})
		stream := &WsStream{Conn: mock}

		buf := make([]byte, 0)
		n, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if n != 0 {
			t.Errorf("Read() n = %v, want 0", n)
		}
	})

	t.Run("EOF after message", func(t *testing.T) {
		mock := newMockConn([]byte{0x01, 0x02})
		stream := &WsStream{Conn: mock}

		buf := make([]byte, 10)
		_, err := stream.Read(buf)
		if err != nil {
			t.Fatalf("First Read() error = %v", err)
		}

		_, err = stream.Read(buf)
		if err != io.EOF {
			t.Errorf("Second Read() error = %v, want io.EOF", err)
		}
	})

	t.Run("NextReader error", func(t *testing.T) {
		mock := &mockWebSocketConn{
			nextReaderErr: errors.New("connection reset"),
		}
		stream := &WsStream{Conn: mock}

		buf := make([]byte, 10)
		_, err := stream.Read(buf)
		if err == nil {
			t.Error("Read() error = nil, want error")
		}
	})

	t.Run("websocket close error", func(t *testing.T) {
		mock := &mockWebSocketConn{
			nextReaderErr: &websocket.CloseError{
				Code: websocket.CloseNormalClosure,
				Text: "normal closure",
			},
		}
		stream := &WsStream{Conn: mock}

		buf := make([]byte, 10)
		_, err := stream.Read(buf)
		if err != io.EOF {
			t.Errorf("Read() error = %v, want io.EOF", err)
		}
	})

	t.Run("concurrent reads", func(t *testing.T) {
		data := []byte{0x01, 0x02, 0x03, 0x04}
		mock := &mockWebSocketConn{
			readData: [][]byte{data, data},
		}
		stream := &WsStream{Conn: mock}

		var wg sync.WaitGroup
		errors := make(chan error, 2)

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 4)
				_, err := stream.Read(buf)
				errors <- err
			}()
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			if err != nil && err != io.EOF {
				t.Errorf("Concurrent Read() error = %v", err)
			}
		}
	})
}

// TestWsStream_Write tests the Write method
func TestWsStream_Write(t *testing.T) {
	t.Run("successful write", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		data := []byte{0x01, 0x02, 0x03}
		n, err := stream.Write(data)

		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if n != 3 {
			t.Errorf("Write() n = %v, want 3", n)
		}
		if len(mock.writeData) != 1 {
			t.Errorf("Write() messages written = %v, want 1", len(mock.writeData))
		}
		if !bytes.Equal(mock.writeData[0], data) {
			t.Errorf("Write() data = %v, want %v", mock.writeData[0], data)
		}
	})

	t.Run("empty write", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		data := []byte{}
		n, err := stream.Write(data)

		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if n != 0 {
			t.Errorf("Write() n = %v, want 0", n)
		}
	})

	t.Run("multiple writes", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		stream.Write([]byte{0x01})
		stream.Write([]byte{0x02})
		stream.Write([]byte{0x03})

		if len(mock.writeData) != 3 {
			t.Errorf("Write() messages written = %v, want 3", len(mock.writeData))
		}
	})

	t.Run("write error", func(t *testing.T) {
		mock := &mockWebSocketConn{
			writeMessageErr: errors.New("write failed"),
		}
		stream := &WsStream{Conn: mock}

		data := []byte{0x01, 0x02}
		_, err := stream.Write(data)

		if err == nil {
			t.Error("Write() error = nil, want error")
		}
	})

	t.Run("websocket close error returns EOF", func(t *testing.T) {
		mock := &mockWebSocketConn{
			writeMessageErr: &websocket.CloseError{
				Code: websocket.CloseGoingAway,
				Text: "going away",
			},
		}
		stream := &WsStream{Conn: mock}

		data := []byte{0x01, 0x02}
		_, err := stream.Write(data)

		if err != io.EOF {
			t.Errorf("Write() error = %v, want io.EOF", err)
		}
	})

	t.Run("concurrent writes", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(b byte) {
				defer wg.Done()
				stream.Write([]byte{b})
			}(byte(i))
		}

		wg.Wait()

		if len(mock.writeData) != 10 {
			t.Errorf("Concurrent Write() messages = %v, want 10", len(mock.writeData))
		}
	})
}

// TestWsStream_Close tests the Close method
func TestWsStream_Close(t *testing.T) {
	t.Run("successful close", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		err := stream.Close()

		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if !mock.closeCalled {
			t.Error("Close() did not call underlying Conn.Close()")
		}
	})

	t.Run("close with error", func(t *testing.T) {
		mock := &mockWebSocketConn{
			closeErr: errors.New("close failed"),
		}
		stream := &WsStream{Conn: mock}

		err := stream.Close()

		if err == nil {
			t.Error("Close() error = nil, want error")
		}
	})

	t.Run("multiple closes", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		stream.Close()
		err := stream.Close()

		// Second close should not panic, just return whatever Conn.Close returns
		if !mock.closeCalled {
			t.Error("Close() did not call underlying Conn.Close()")
		}
		_ = err // We don't care about the error on second close
	})
}

// TestWsStream_ReadWrite tests full read-write cycle
func TestWsStream_ReadWrite(t *testing.T) {
	t.Run("full cycle", func(t *testing.T) {
		mock := newMockConn(nil)
		stream := &WsStream{Conn: mock}

		// Write some data
		writeData := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		_, err := stream.Write(writeData)
		if err != nil {
			t.Fatalf("Write() error = %v", err)
		}

		// Verify write
		if len(mock.writeData) != 1 {
			t.Fatalf("Write() messages = %v, want 1", len(mock.writeData))
		}

		// Now test reading
		readMock := &mockWebSocketConn{
			readData: [][]byte{writeData},
		}
		readStream := &WsStream{Conn: readMock}

		buf := make([]byte, 10)
		n, err := readStream.Read(buf)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}

		if n != 5 {
			t.Errorf("Read() n = %v, want 5", n)
		}
		if !bytes.Equal(buf[:5], writeData) {
			t.Errorf("Read() data = %v, want %v", buf[:5], writeData)
		}
	})
}

// BenchmarkWsStream_Read benchmarks the Read method
func BenchmarkWsStream_Read(b *testing.B) {
	data := make([]byte, 1024)
	mock := newMockConn(data)
	stream := &WsStream{Conn: mock}

	buf := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stream.Read(buf)
		// Reset for next iteration
		if i%1000 == 999 {
			mock.readIndex = 0
		}
	}
}

// BenchmarkWsStream_Write benchmarks the Write method
func BenchmarkWsStream_Write(b *testing.B) {
	mock := newMockConn(nil)
	stream := &WsStream{Conn: mock}

	data := make([]byte, 1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stream.Write(data)
	}
}
