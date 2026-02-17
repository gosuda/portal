package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerBehavior(t *testing.T) {
	tests := []struct {
		name            string
		prefix          string
		preCloseContext bool
		waitForResult   bool
		minAttempts     uint64
	}{
		{
			name:            "closed_context_exits_immediately",
			prefix:          "",
			preCloseContext: true,
		},
		{
			name:          "empty_prefix_reports_match",
			prefix:        "",
			waitForResult: true,
		},
		{
			name:        "invalid_prefix_never_matches",
			prefix:      "!",
			minAttempts: 10,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts uint64
			var found uint64
			results := make(chan *Result, 1)
			ctx := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)

			if tc.preCloseContext {
				close(ctx)
			}

			go worker(tc.prefix, &attempts, &found, results, &wg, ctx)

			var got *Result
			if tc.waitForResult {
				select {
				case got = <-results:
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for worker result")
				}
				close(ctx)
			} else if !tc.preCloseContext {
				waitForAttempts(t, &attempts, tc.minAttempts, 3*time.Second)
				close(ctx)
			}

			waitForWaitGroup(t, &wg, 3*time.Second)

			switch tc.name {
			case "closed_context_exits_immediately":
				if gotAttempts := atomic.LoadUint64(&attempts); gotAttempts != 0 {
					t.Fatalf("attempts = %d, want 0", gotAttempts)
				}
				if gotFound := atomic.LoadUint64(&found); gotFound != 0 {
					t.Fatalf("found = %d, want 0", gotFound)
				}
				select {
				case result := <-results:
					t.Fatalf("unexpected result: %+v", result)
				default:
				}
			case "empty_prefix_reports_match":
				if got == nil {
					t.Fatal("result is nil")
				}
				if got.ID == "" {
					t.Fatal("result ID is empty")
				}
				if got.Attempt == 0 {
					t.Fatal("result attempt is zero")
				}
				if len(got.PrivateKey) != 32 {
					t.Fatalf("private key length = %d, want 32", len(got.PrivateKey))
				}
				if len(got.PublicKey) != 32 {
					t.Fatalf("public key length = %d, want 32", len(got.PublicKey))
				}
				if gotAttempts := atomic.LoadUint64(&attempts); gotAttempts < got.Attempt {
					t.Fatalf("attempts = %d, want >= result attempt %d", gotAttempts, got.Attempt)
				}
				if gotFound := atomic.LoadUint64(&found); gotFound < 1 {
					t.Fatalf("found = %d, want >= 1", gotFound)
				}
			case "invalid_prefix_never_matches":
				if gotFound := atomic.LoadUint64(&found); gotFound != 0 {
					t.Fatalf("found = %d, want 0", gotFound)
				}
				if gotAttempts := atomic.LoadUint64(&attempts); gotAttempts < tc.minAttempts {
					t.Fatalf("attempts = %d, want >= %d", gotAttempts, tc.minAttempts)
				}
				select {
				case result := <-results:
					t.Fatalf("unexpected result for invalid prefix: %+v", result)
				default:
				}
			}
		})
	}
}

func TestStatsReporterStopsOnDoneSignal(t *testing.T) {
	origStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = writePipe
	t.Cleanup(func() {
		os.Stdout = origStdout
		_ = readPipe.Close()
	})

	done := make(chan bool)
	var attempts uint64 = 100
	var found uint64 = 1

	finished := make(chan struct{})
	go func() {
		statsReporter(&attempts, &found, time.Now(), done, 1, 1)
		close(finished)
	}()

	done <- true

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("statsReporter did not stop after done signal")
	}

	closeErr := writePipe.Close()
	if closeErr != nil {
		t.Fatalf("writePipe.Close() error = %v", closeErr)
	}

	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if len(output) == 0 {
		t.Fatal("expected newline output on shutdown, got empty output")
	}
}

func TestCLIBehavior(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		timeout      time.Duration
		wantErr      bool
		wantContains []string
	}{
		{
			name:    "single_result_lowercase_prefix_is_uppercased",
			args:    []string{"-prefix=aaa", "-workers=1", "-max=1"},
			timeout: 30 * time.Second,
			wantContains: []string{
				"Searching for IDs with prefix: AAA (3 characters)",
				"Using 1 parallel workers",
				"Max results: 1",
				"=== Final Stats ===",
			},
		},
		{
			name:    "negative_workers_panics",
			args:    []string{"-prefix=aaa", "-workers=-1", "-max=1"},
			timeout: 10 * time.Second,
			wantErr: true,
			wantContains: []string{
				"Using -1 parallel workers",
				"panic:",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, "go", append([]string{"run", "."}, tc.args...)...)
			cmd.Dir = "."

			out, err := cmd.CombinedOutput()
			if ctx.Err() == context.DeadlineExceeded {
				t.Fatalf("go run timed out after %s. output:\n%s", tc.timeout, out)
			}

			if tc.wantErr && err == nil {
				t.Fatalf("expected command error, got nil. output:\n%s", out)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected command success, got error %v. output:\n%s", err, out)
			}

			output := string(out)
			for _, want := range tc.wantContains {
				if !strings.Contains(output, want) {
					t.Fatalf("output missing %q. full output:\n%s", want, output)
				}
			}
		})
	}
}

func waitForAttempts(t *testing.T, attempts *uint64, minAttempts uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadUint64(attempts) >= minAttempts {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("attempts did not reach %d within %s (got %d)", minAttempts, timeout, atomic.LoadUint64(attempts))
}

func waitForWaitGroup(t *testing.T, wg *sync.WaitGroup, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("worker did not exit within %s", timeout)
	}
}
