package sdk

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func TestRunHTTPRelayOnly(t *testing.T) {
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
			t.Fatalf("RunHTTP() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTP() did not exit after context cancellation")
	}
}

func TestExposureRunHTTPLocalOnly(t *testing.T) {
	t.Parallel()

	localListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	localAddr := localListener.Addr().String()
	_ = localListener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var exposure *Exposure
	errCh := make(chan error, 1)
	go func() {
		errCh <- exposure.RunHTTP(ctx, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), localAddr)
	}()

	waitForHTTP(t, "http://"+localAddr)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunHTTP() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTP() did not exit after context cancellation")
	}
}

func TestRunHTTPRequiresRelayOrLocal(t *testing.T) {
	t.Parallel()

	err := RunHTTP(context.Background(), nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "")
	if err == nil {
		t.Fatal("RunHTTP() error = nil, want error")
	}
}

func TestRunHTTPLocalAndRelay(t *testing.T) {
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
			t.Fatalf("RunHTTP() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTP() did not exit after context cancellation")
	}
}

func TestRunHTTPRelayListenerCloseIsNormal(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHTTP(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}), "")
	}()

	waitForHTTP(t, "http://"+listener.Addr().String())

	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunHTTP() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTP() did not exit after listener close")
	}
}

func TestRunHTTPRelayListenerCloseKeepsLocalRunning(t *testing.T) {
	t.Parallel()

	relayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}

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

	if err := relayListener.Close(); err != nil {
		t.Fatalf("relayListener.Close() error = %v", err)
	}

	waitForHTTP(t, "http://"+localAddr)

	select {
	case err := <-errCh:
		t.Fatalf("RunHTTP() exited early with %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunHTTP() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunHTTP() did not exit after context cancellation")
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

	listener, err := mergeListeners()
	if err == nil {
		t.Fatal("mergeListeners() error = nil, want error")
	}
	if listener != nil {
		t.Fatalf("mergeListeners() listener = %#v, want nil", listener)
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

	merged, err := mergeListeners(listener1, listener2)
	if err != nil {
		t.Fatalf("mergeListeners() error = %v", err)
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

	merged, err := mergeListeners(listener1, listener2)
	if err != nil {
		t.Fatalf("mergeListeners() error = %v", err)
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

	merged, err := mergeListeners(listener)
	if err != nil {
		t.Fatalf("mergeListeners() error = %v", err)
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

func TestMergedListenerAcceptDoesNotReturnQueuedConnAfterClose(t *testing.T) {
	t.Parallel()

	conn := &stubConn{}
	closed := make(chan struct{})
	close(closed)

	merged := &mergedListener{
		accepted: make(chan net.Conn, 1),
		closed:   closed,
	}
	merged.accepted <- conn

	gotConn, err := merged.Accept()
	if gotConn != nil {
		t.Fatalf("Accept() conn = %#v, want nil", gotConn)
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept() error = %v, want net.ErrClosed", err)
	}
	if conn.closeCount != 1 {
		t.Fatalf("conn close count = %d, want 1", conn.closeCount)
	}
}

func TestNormalizeRelayURLs(t *testing.T) {
	t.Parallel()

	got, err := NormalizeRelayURLs([]string{
		" localhost:4017 , https://relay.example.com/base/relay?x=1#frag ",
		"https://relay.example.com/base",
	})
	if err != nil {
		t.Fatalf("NormalizeRelayURLs() error = %v", err)
	}

	want := []string{
		"https://localhost:4017",
		"https://relay.example.com/base",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeRelayURLs() = %v, want %v", got, want)
	}
}

func TestNormalizeRelayURLRejectsNonHTTPS(t *testing.T) {
	t.Parallel()

	_, err := NormalizeRelayURL("http://relay.example.com")
	if err == nil {
		t.Fatal("NormalizeRelayURL() error = nil, want error")
	}
}

func TestNormalizeTargetAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "host only",
			input: "localhost",
			want:  "localhost:80",
		},
		{
			name:  "host and port",
			input: "127.0.0.1:8080",
			want:  "127.0.0.1:8080",
		},
		{
			name:  "http url",
			input: "http://localhost:3000",
			want:  "localhost:3000",
		},
		{
			name:  "https url default port preserved by host parsing",
			input: "https://example.com",
			want:  "example.com:80",
		},
		{
			name:  "ipv6 host",
			input: "::1",
			want:  "[::1]:80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeTargetAddr(tt.input)
			if err != nil {
				t.Fatalf("NormalizeTargetAddr() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeTargetAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeTargetAddrRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"",
		"ftp://example.com",
		"http://example.com/path",
		"http://example.com?a=1",
		"http://example.com#frag",
		"host:port:extra",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			if _, err := NormalizeTargetAddr(input); err == nil {
				t.Fatalf("NormalizeTargetAddr(%q) error = nil, want error", input)
			}
		})
	}
}

func TestExposeNoRelayInputs(t *testing.T) {
	t.Parallel()

	exposure, err := Expose(context.Background(), nil, "demo", types.LeaseMetadata{})
	if err != nil {
		t.Fatalf("Expose() error = %v", err)
	}
	if exposure != nil {
		t.Fatalf("Expose() exposure = %#v, want nil", exposure)
	}
}

func TestExposureAccessorsReturnCopies(t *testing.T) {
	t.Parallel()

	exposure := &Exposure{
		relays: []exposureRelay{
			{
				relayURL:   "https://relay-1.example.com",
				publicURLs: []string{"https://app.example.com"},
			},
			{
				relayURL:   "https://relay-2.example.com",
				publicURLs: []string{"https://app.example.com"},
			},
		},
	}

	relayURLs := exposure.RelayURLs()
	publicURLs := exposure.PublicURLs()

	relayURLs[0] = "changed"
	publicURLs[0] = "changed"

	if got, want := exposure.RelayURLs(), []string{"https://relay-1.example.com", "https://relay-2.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RelayURLs() = %v, want %v", got, want)
	}
	if got, want := exposure.PublicURLs(), []string{"https://app.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PublicURLs() = %v, want %v", got, want)
	}
	if got, want := exposure.relays[0].relayURL, "https://relay-1.example.com"; got != want {
		t.Fatalf("relays[0].relayURL = %q, want %q", got, want)
	}
	if got, want := exposure.relays[0].publicURLs, []string{"https://app.example.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("relays[0].publicURLs = %v, want %v", got, want)
	}
}

func TestExposureCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	listener := &stubListener{}
	exposure := &Exposure{listener: listener}

	if err := exposure.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := exposure.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if listener.closeCount != 1 {
		t.Fatalf("listener close count = %d, want 1", listener.closeCount)
	}
}

func TestExposureImplementsListener(t *testing.T) {
	t.Parallel()

	listener := &stubListener{addr: listenerAddr("merged:test")}
	exposure := &Exposure{listener: listener}

	if got := exposure.Addr().String(); got != "merged:test" {
		t.Fatalf("Addr().String() = %q, want %q", got, "merged:test")
	}

	_, err := exposure.Accept()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept() error = %v, want net.ErrClosed", err)
	}
}

type stubListener struct {
	closeCount int
	addr       net.Addr
}

func (l *stubListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (l *stubListener) Close() error {
	l.closeCount++
	if l.closeCount > 1 {
		return errors.New("listener closed more than once")
	}
	return nil
}

func (l *stubListener) Addr() net.Addr {
	if l.addr != nil {
		return l.addr
	}
	return listenerAddr("stub")
}

type stubConn struct {
	closeCount int
}

func (c *stubConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *stubConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *stubConn) Close() error                       { c.closeCount++; return nil }
func (c *stubConn) LocalAddr() net.Addr                { return listenerAddr("stub-local") }
func (c *stubConn) RemoteAddr() net.Addr               { return listenerAddr("stub-remote") }
func (c *stubConn) SetDeadline(_ time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(_ time.Time) error { return nil }
