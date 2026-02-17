package manager

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestBPSManagerSetGetAndCleanup(t *testing.T) {
	t.Parallel()

	m := NewBPSManager()
	const leaseID = "lease-a"

	if got := m.GetBPSLimit(leaseID); got != 0 {
		t.Fatalf("GetBPSLimit() = %d, want 0 for unknown lease", got)
	}

	m.SetBPSLimit(leaseID, 2048)
	if got := m.GetBPSLimit(leaseID); got != 2048 {
		t.Fatalf("GetBPSLimit() = %d, want 2048", got)
	}

	all := m.GetAllBPSLimits()
	if got := all[leaseID]; got != 2048 {
		t.Fatalf("GetAllBPSLimits()[%q] = %d, want 2048", leaseID, got)
	}
	all[leaseID] = 1
	if got := m.GetBPSLimit(leaseID); got != 2048 {
		t.Fatalf("GetAllBPSLimits() should return copy; manager value = %d, want 2048", got)
	}

	m.CleanupLease(leaseID)
	if got := m.GetBPSLimit(leaseID); got != 0 {
		t.Fatalf("GetBPSLimit() after cleanup = %d, want 0", got)
	}
	if got := m.GetBucket(leaseID); got != nil {
		t.Fatalf("GetBucket() after cleanup = %v, want nil", got)
	}
}

func TestBPSManagerSetBPSLimitClearsOnNonPositive(t *testing.T) {
	t.Parallel()

	m := NewBPSManager()
	const leaseID = "lease-clear"

	m.SetBPSLimit(leaseID, 1024)
	if got := m.GetBPSLimit(leaseID); got != 1024 {
		t.Fatalf("GetBPSLimit() = %d, want 1024", got)
	}

	m.SetBPSLimit(leaseID, 0)
	if got := m.GetBPSLimit(leaseID); got != 0 {
		t.Fatalf("GetBPSLimit() after zero = %d, want 0", got)
	}
	if got := m.GetBucket(leaseID); got != nil {
		t.Fatalf("GetBucket() after zero limit = %v, want nil", got)
	}

	m.SetBPSLimit(leaseID, 1024)
	m.SetBPSLimit(leaseID, -10)
	if got := m.GetBPSLimit(leaseID); got != 0 {
		t.Fatalf("GetBPSLimit() after negative = %d, want 0", got)
	}
}

func TestBPSManagerDefaultBPSNormalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   int64
		wantBPS int64
	}{
		{name: "negative becomes zero", input: -1, wantBPS: 0},
		{name: "zero remains zero", input: 0, wantBPS: 0},
		{name: "positive remains unchanged", input: 4096, wantBPS: 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := NewBPSManager()
			m.SetDefaultBPS(tt.input)
			if got := m.GetDefaultBPS(); got != tt.wantBPS {
				t.Fatalf("GetDefaultBPS() = %d, want %d", got, tt.wantBPS)
			}
		})
	}
}

func TestBPSManagerGetBucketLifecycle(t *testing.T) {
	t.Parallel()

	m := NewBPSManager()
	const leaseID = "lease-bucket"

	if got := m.GetBucket(leaseID); got != nil {
		t.Fatalf("GetBucket() for unlimited lease = %v, want nil", got)
	}

	m.SetBPSLimit(leaseID, 1024)
	first := m.GetBucket(leaseID)
	if first == nil {
		t.Fatal("GetBucket() returned nil for configured limit")
	}
	if got := first.Rate(); got != 1024 {
		t.Fatalf("bucket.Rate() = %d, want 1024", got)
	}

	second := m.GetBucket(leaseID)
	if second != first {
		t.Fatal("GetBucket() should reuse existing bucket for unchanged limit")
	}

	m.SetBPSLimit(leaseID, 2048)
	third := m.GetBucket(leaseID)
	if third == nil {
		t.Fatal("GetBucket() returned nil after limit update")
	}
	if third == first {
		t.Fatal("GetBucket() should create new bucket when limit changes")
	}
	if got := third.Rate(); got != 2048 {
		t.Fatalf("updated bucket.Rate() = %d, want 2048", got)
	}
}

func TestBPSManagerConcurrentGetBucket(t *testing.T) {
	t.Parallel()

	const workers = 32
	m := NewBPSManager()
	m.SetBPSLimit("lease-concurrent", 4096)

	var wg sync.WaitGroup
	buckets := make(chan *Bucket, workers)

	for range workers {
		wg.Go(func() {
			buckets <- m.GetBucket("lease-concurrent")
		})
	}
	wg.Wait()
	close(buckets)

	var first *Bucket
	for b := range buckets {
		if b == nil {
			t.Fatal("GetBucket() returned nil in concurrent path")
		}
		if first == nil {
			first = b
			continue
		}
		if b != first {
			t.Fatal("GetBucket() returned different bucket instances concurrently")
		}
	}
}

func TestBPSManagerCopy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		leaseID string
		limit   int64
	}{
		{name: "unlimited lease copy", leaseID: "lease-unlimited", limit: 0},
		{name: "limited lease copy", leaseID: "lease-limited", limit: 1024 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := NewBPSManager()
			if tt.limit > 0 {
				m.SetBPSLimit(tt.leaseID, tt.limit)
			}

			srcData := []byte("hello portal relay")
			src := bytes.NewReader(srcData)
			var dst bytes.Buffer

			n, err := m.Copy(&dst, src, tt.leaseID)
			if err != nil {
				t.Fatalf("Copy() error = %v, want nil", err)
			}
			if n != int64(len(srcData)) {
				t.Fatalf("Copy() bytes = %d, want %d", n, len(srcData))
			}
			if got := dst.String(); got != string(srcData) {
				t.Fatalf("copy output = %q, want %q", got, string(srcData))
			}

			if tt.limit > 0 {
				b := m.GetBucket(tt.leaseID)
				if b == nil {
					t.Fatal("expected bucket for limited lease")
				}
				totalBytes, _, _ := b.Stats()
				if totalBytes != n {
					t.Fatalf("bucket total bytes = %d, want %d", totalBytes, n)
				}
			}
		})
	}
}

func TestEstablishRelayWithBPS(t *testing.T) {
	t.Parallel()

	clientPayload := []byte("client-to-lease")
	leasePayload := []byte("lease-to-client")

	clientStream := newScriptedRelayStream(scriptedRead{
		data: clientPayload,
		err:  io.EOF,
	})
	leaseStream := newScriptedRelayStream(scriptedRead{
		data: leasePayload,
		err:  errors.New("lease read failed"),
	})

	done := make(chan struct{})
	go func() {
		EstablishRelayWithBPS(clientStream, leaseStream, "lease-establish", NewBPSManager())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EstablishRelayWithBPS did not return")
	}

	if got := leaseStream.CloseCalls(); got == 0 {
		t.Fatal("lease stream was not closed")
	}
	if got := clientStream.CloseCalls(); got == 0 {
		t.Fatal("client stream was not closed")
	}
	if got := leaseStream.WrittenData(); !bytes.Equal(got, clientPayload) {
		t.Fatalf("lease stream wrote %q, want %q", got, clientPayload)
	}
	if got := clientStream.WrittenData(); !bytes.Equal(got, leasePayload) {
		t.Fatalf("client stream wrote %q, want %q", got, leasePayload)
	}
}

func TestNewBucketValidationAndDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rate      int64
		burst     int64
		wantNil   bool
		wantBurst int64
	}{
		{name: "zero rate", rate: 0, burst: 10, wantNil: true},
		{name: "negative rate", rate: -1, burst: 10, wantNil: true},
		{name: "zero burst defaults to rate", rate: 1000, burst: 0, wantNil: false, wantBurst: 1000},
		{name: "negative burst defaults to rate", rate: 1000, burst: -50, wantNil: false, wantBurst: 1000},
		{name: "custom burst", rate: 1000, burst: 250, wantNil: false, wantBurst: 250},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := NewBucket(tt.rate, tt.burst)
			if tt.wantNil {
				if b != nil {
					t.Fatalf("NewBucket(%d, %d) = %v, want nil", tt.rate, tt.burst, b)
				}
				return
			}
			if b == nil {
				t.Fatalf("NewBucket(%d, %d) returned nil", tt.rate, tt.burst)
			}
			if got := b.Rate(); got != tt.rate {
				t.Fatalf("bucket.Rate() = %d, want %d", got, tt.rate)
			}
			if got := int64(b.maxTokens); got != tt.wantBurst {
				t.Fatalf("bucket maxTokens = %d, want %d", got, tt.wantBurst)
			}
		})
	}
}

func TestBucketTakeAndStats(t *testing.T) {
	t.Parallel()

	b := NewBucket(1024*1024, 1024*1024)
	if b == nil {
		t.Fatal("NewBucket() returned nil")
	}

	b.Take(0)
	b.Take(-1)
	total, hits, waited := b.Stats()
	if total != 0 || hits != 0 || waited != 0 {
		t.Fatalf("stats after no-op takes = (%d, %d, %v), want (0, 0, 0)", total, hits, waited)
	}

	b.Take(512)
	total, hits, _ = b.Stats()
	if total != 512 {
		t.Fatalf("stats total bytes = %d, want 512", total)
	}
	if hits != 0 {
		t.Fatalf("stats throttle hits = %d, want 0 with full initial burst", hits)
	}

	available := b.Available()
	if available <= 0 || available > b.maxTokens {
		t.Fatalf("Available() = %f, want in range (0,%f]", available, b.maxTokens)
	}
}

func TestBucketTakeWithTimeout(t *testing.T) {
	t.Parallel()

	if ok := (*Bucket)(nil).TakeWithTimeout(10, 0); !ok {
		t.Fatal("nil bucket should return true")
	}

	b := NewBucket(1, 1)
	if b == nil {
		t.Fatal("NewBucket() returned nil")
	}

	if ok := b.TakeWithTimeout(0, 0); !ok {
		t.Fatal("TakeWithTimeout(0, ...) should return true")
	}

	// Consume current token, then request more than can be served with maxWait=0.
	b.Take(1)
	if ok := b.TakeWithTimeout(2, 0); ok {
		t.Fatal("TakeWithTimeout should return false when maxWait=0 and tokens are insufficient")
	}
}

func TestBucketConcurrentTake(t *testing.T) {
	t.Parallel()

	const (
		workers   = 32
		bytesEach = int64(256)
	)
	b := NewBucket(1024*1024, 1024*1024)
	if b == nil {
		t.Fatal("NewBucket() returned nil")
	}

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			b.Take(bytesEach)
		})
	}
	wg.Wait()

	total, _, _ := b.Stats()
	wantTotal := int64(workers) * bytesEach
	if total != wantTotal {
		t.Fatalf("total bytes after concurrent take = %d, want %d", total, wantTotal)
	}
}

func TestCopyNilBucket(t *testing.T) {
	t.Parallel()

	src := bytes.NewReader([]byte("copy without bucket"))
	var dst bytes.Buffer

	n, err := Copy(&dst, src, nil)
	if err != nil {
		t.Fatalf("Copy() error = %v, want nil", err)
	}
	if n != int64(dst.Len()) {
		t.Fatalf("Copy() bytes = %d, want %d", n, dst.Len())
	}
}

func TestCopyErrorPaths(t *testing.T) {
	t.Parallel()

	readErr := errors.New("read failed")
	writeErr := errors.New("write failed")

	tests := []struct {
		name      string
		dst       io.Writer
		src       io.Reader
		wantErr   error
		wantBytes int64
		useBucket bool
	}{
		{
			name:      "read error",
			dst:       &bytes.Buffer{},
			src:       &alwaysErrReader{err: readErr},
			wantErr:   readErr,
			wantBytes: 0,
			useBucket: true,
		},
		{
			name:      "write error",
			dst:       &alwaysErrWriter{err: writeErr},
			src:       bytes.NewReader([]byte("payload")),
			wantErr:   writeErr,
			wantBytes: 0,
			useBucket: true,
		},
		{
			name:      "short write",
			dst:       &shortWriteWriter{maxWrite: 2},
			src:       bytes.NewReader([]byte("payload")),
			wantErr:   io.ErrShortWrite,
			wantBytes: 2,
			useBucket: true,
		},
		{
			name:      "read data then error",
			dst:       &bytes.Buffer{},
			src:       &readThenErrReader{data: []byte("ok"), err: readErr},
			wantErr:   readErr,
			wantBytes: 2,
			useBucket: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b *Bucket
			if tt.useBucket {
				b = NewBucket(1024*1024, 1024*1024)
				if b == nil {
					t.Fatal("NewBucket() returned nil")
				}
			}

			n, err := Copy(tt.dst, tt.src, b)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Copy() error = %v, want %v", err, tt.wantErr)
			}
			if n != tt.wantBytes {
				t.Fatalf("Copy() bytes = %d, want %d", n, tt.wantBytes)
			}
		})
	}
}

func TestCopyEOFCompletesWithoutError(t *testing.T) {
	t.Parallel()

	src := bytes.NewReader([]byte("abc"))
	var dst bytes.Buffer
	b := NewBucket(1024*1024, 1024*1024)
	if b == nil {
		t.Fatal("NewBucket() returned nil")
	}

	n, err := Copy(&dst, src, b)
	if err != nil {
		t.Fatalf("Copy() error = %v, want nil", err)
	}
	if n != 3 {
		t.Fatalf("Copy() bytes = %d, want 3", n)
	}
	if got := dst.String(); got != "abc" {
		t.Fatalf("copy output = %q, want %q", got, "abc")
	}
}

type scriptedRead struct {
	data []byte
	err  error
}

type scriptedRelayStream struct {
	mu sync.Mutex

	readScript []scriptedRead
	readIdx    int

	writes     bytes.Buffer
	closeCalls int
}

func newScriptedRelayStream(steps ...scriptedRead) *scriptedRelayStream {
	script := make([]scriptedRead, 0, len(steps))
	for _, step := range steps {
		script = append(script, scriptedRead{
			data: append([]byte(nil), step.data...),
			err:  step.err,
		})
	}
	return &scriptedRelayStream{readScript: script}
}

func (s *scriptedRelayStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.readIdx >= len(s.readScript) {
		return 0, io.EOF
	}

	step := s.readScript[s.readIdx]
	s.readIdx++
	n := copy(p, step.data)
	return n, step.err
}

func (s *scriptedRelayStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes.Write(p)
}

func (s *scriptedRelayStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return nil
}

func (s *scriptedRelayStream) CloseCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

func (s *scriptedRelayStream) WrittenData() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.writes.Bytes()...)
}

type alwaysErrReader struct {
	err error
}

func (r *alwaysErrReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

type readThenErrReader struct {
	data []byte
	err  error
	read bool
}

func (r *readThenErrReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, r.err
	}
	r.read = true
	n := copy(p, r.data)
	return n, nil
}

type shortWriteWriter struct {
	maxWrite int
}

func (w *shortWriteWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		return w.maxWrite, nil
	}
	return len(p), nil
}

type alwaysErrWriter struct {
	err error
}

func (w *alwaysErrWriter) Write(_ []byte) (int, error) {
	return 0, w.err
}

func TestBucketStatsWaitedType(t *testing.T) {
	t.Parallel()

	b := NewBucket(1, 1)
	if b == nil {
		t.Fatal("NewBucket() returned nil")
	}
	b.Take(1)

	ok := b.TakeWithTimeout(2, time.Nanosecond)
	if ok {
		t.Fatal("expected timeout with very small wait budget")
	}
	_, _, waited := b.Stats()
	if waited < 0 {
		t.Fatalf("total waited should never be negative, got %v", waited)
	}
}
