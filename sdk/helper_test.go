package sdk

import (
	"context"
	"io"
	"net"
	"net/http"
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
		errCh <- RunHTTPApp(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), HTTPServeOptions{})
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
		errCh <- RunHTTPApp(ctx, relayListener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), HTTPServeOptions{
			LocalAddr: localAddr,
		})
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

func TestSplitCSV(t *testing.T) {
	t.Parallel()

	got := SplitCSV(" a, ,b,c ,, d ")
	want := []string{"a", "b", "c", "d"}

	if len(got) != len(want) {
		t.Fatalf("SplitCSV() len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SplitCSV()[%d] = %q, want %q", i, got[i], want[i])
		}
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
