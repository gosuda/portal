package sdk

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"testing"
	"time"
)

func TestRunHTTPAppRelayOnly(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHTTP(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), "")
	}()

	waitForHTTP(t, "http://"+listener.Addr().String())
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunHTTPApp() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTPApp() did not exit after context cancellation")
	}
}

func TestRunHTTPAppLocalAndRelay(t *testing.T) {
	t.Parallel()

	relayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer relayListener.Close()

	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	localAddr := localListener.Addr().String()
	_ = localListener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHTTP(ctx, relayListener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), localAddr)
	}()

	waitForHTTP(t, "http://"+relayListener.Addr().String())
	waitForHTTP(t, "http://"+localAddr)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunHTTPApp() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTPApp() did not exit after context cancellation")
	}
}

func waitForHTTP(t *testing.T, rawURL string) {
	t.Helper()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(rawURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", rawURL)
}

func TestMergeListenersRequiresInput(t *testing.T) {
	t.Parallel()

	listener, err := MergeListeners()
	if err == nil {
		t.Fatal("MergeListeners() error = nil, want error")
	}
	if listener != nil {
		t.Fatalf("MergeListeners() listener = %#v, want nil", listener)
	}
}

func TestMergeListenersAcceptsFromAllSources(t *testing.T) {
	t.Parallel()

	listener1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener1.Close()

	listener2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener2.Close()

	merged, err := MergeListeners(listener1, listener2)
	if err != nil {
		t.Fatalf("MergeListeners() error = %v", err)
	}
	defer merged.Close()

	client1, err := net.Dial("tcp", listener1.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client1.Close()

	client2, err := net.Dial("tcp", listener2.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client2.Close()

	got := make([]string, 0, 2)
	for range 2 {
		conn, err := merged.Accept()
		if err != nil {
			t.Fatalf("Accept() error = %v", err)
		}
		got = append(got, conn.LocalAddr().String())
		_ = conn.Close()
	}

	want := []string{listener1.Addr().String(), listener2.Addr().String()}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("accepted local addrs = %v, want %v", got, want)
	}
}

func TestMergeListenersContinuesAfterOneSourceCloses(t *testing.T) {
	t.Parallel()

	listener1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener1.Close()

	listener2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener2.Close()

	merged, err := MergeListeners(listener1, listener2)
	if err != nil {
		t.Fatalf("MergeListeners() error = %v", err)
	}
	defer merged.Close()

	if err := listener1.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener1.Close() error = %v", err)
	}

	acceptCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, acceptErr := merged.Accept()
		if acceptErr != nil {
			errCh <- acceptErr
			return
		}
		acceptCh <- conn
	}()

	client, err := net.Dial("tcp", listener2.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()

	select {
	case acceptErr := <-errCh:
		t.Fatalf("Accept() error = %v", acceptErr)
	case conn := <-acceptCh:
		if conn.LocalAddr().String() != listener2.Addr().String() {
			t.Fatalf("conn.LocalAddr() = %q, want %q", conn.LocalAddr().String(), listener2.Addr().String())
		}
		_ = conn.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("Accept() did not return connection from surviving listener")
	}
}

func TestMergeListenersCloseUnblocksAccept(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	merged, err := MergeListeners(listener)
	if err != nil {
		t.Fatalf("MergeListeners() error = %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, acceptErr := merged.Accept()
		errCh <- acceptErr
	}()

	if err := merged.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case acceptErr := <-errCh:
		if !errors.Is(acceptErr, net.ErrClosed) {
			t.Fatalf("Accept() error = %v, want net.ErrClosed", acceptErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Accept() did not unblock after Close()")
	}
}
