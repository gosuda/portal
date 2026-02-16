package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/utils/randpool"
)

func main() {
	prefix := flag.String("prefix", "CHAT", "ID prefix to search for")
	workers := flag.Int("workers", runtime.NumCPU(), "Number of parallel workers")
	maxResults := flag.Int("max", 1, "Maximum number of results to find (0 = unlimited)")
	flag.Parse()

	// Convert prefix to uppercase (base32 encoding is uppercase)
	*prefix = strings.ToUpper(*prefix)

	// Calculate expected attempts (base32 has 32 characters)
	expectedAttempts := math.Pow(32, float64(len(*prefix)))

	fmt.Printf("Searching for IDs with prefix: %s (%d characters)\n", *prefix, len(*prefix))
	fmt.Printf("Using %d parallel workers\n", *workers)
	fmt.Printf("Max results: %d\n", *maxResults)
	fmt.Printf("Expected attempts per result: %.0f (average)\n", expectedAttempts/2)
	fmt.Println()

	var (
		attempts  uint64
		found     uint64
		startTime = time.Now()
		results   = make(chan *Result, *workers)
		wg        sync.WaitGroup
		ctx       = make(chan struct{}) // Context for stopping workers
	)

	// Start worker goroutines
	for range *workers {
		wg.Add(1)
		go worker(*prefix, &attempts, &found, results, &wg, ctx)
	}

	// Start stats reporter
	done := make(chan bool)
	go statsReporter(&attempts, &found, startTime, done, len(*prefix), *maxResults)

	// Collect and print results
	foundCount := 0
	for result := range results {
		foundCount++
		elapsed := time.Since(startTime)
		fmt.Printf("\n[#%d] Found at %.2fs (attempt #%d):\n", foundCount, elapsed.Seconds(), result.Attempt)
		fmt.Printf("  ID:         %s\n", result.ID)
		fmt.Printf("  PrivateKey: %s\n", base64.StdEncoding.EncodeToString(result.PrivateKey))
		fmt.Printf("  PublicKey:  %s\n", base64.StdEncoding.EncodeToString(result.PublicKey))
		fmt.Println()

		// If we've reached max results, signal workers to stop
		if *maxResults > 0 && foundCount >= *maxResults {
			close(ctx)
			// Wait for all workers to finish
			go func() {
				wg.Wait()
				close(results)
			}()
		}
	}

	done <- true
	elapsed := time.Since(startTime)
	fmt.Printf("\n=== Final Stats ===\n")
	fmt.Printf("Total attempts: %d\n", atomic.LoadUint64(&attempts))
	fmt.Printf("Total found:    %d\n", foundCount)
	fmt.Printf("Elapsed time:   %.2fs\n", elapsed.Seconds())
	fmt.Printf("Rate:           %.0f attempts/sec\n", float64(atomic.LoadUint64(&attempts))/elapsed.Seconds())
}

type Result struct {
	ID         string
	PrivateKey []byte
	PublicKey  []byte
	Attempt    uint64
}

func worker(prefix string, attempts, found *uint64, results chan<- *Result, wg *sync.WaitGroup, ctx <-chan struct{}) {
	defer wg.Done()

	var seed [32]byte

	for {
		// Check if we should stop
		select {
		case <-ctx:
			return
		default:
		}

		// Generate random seed using randpool
		randpool.Rand(seed[:])

		cred, err := cryptoops.NewCredentialFromPrivateKey(seed[:])
		if err != nil {
			continue
		}

		privateKey := cred.X25519PrivateKey()
		publicKey := cred.PublicKey()
		id := cred.ID()

		// Increment attempts counter
		attemptNum := atomic.AddUint64(attempts, 1)

		// Check if ID starts with the desired prefix
		if strings.HasPrefix(id, prefix) {
			// Increment found counter
			atomic.AddUint64(found, 1)

			// Try to send result, but return if context is closed
			select {
			case results <- &Result{
				ID:         id,
				PrivateKey: privateKey,
				PublicKey:  publicKey,
				Attempt:    attemptNum,
			}:
			case <-ctx:
				return
			}
		}
	}
}

func statsReporter(attempts, found *uint64, startTime time.Time, done <-chan bool, prefixLen, maxResults int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Calculate expected attempts per result
	expectedAttemptsPerResult := math.Pow(32, float64(prefixLen)) / 2

	for {
		select {
		case <-ticker.C:
			elapsed := time.Since(startTime)
			a := atomic.LoadUint64(attempts)
			f := atomic.LoadUint64(found)
			rate := float64(a) / elapsed.Seconds()

			// Calculate estimated time to completion
			var etaStr string
			if rate > 0 && maxResults > 0 {
				maxResultsUint64 := uint64(maxResults)
				if f < maxResultsUint64 {
					remainingResults := maxResultsUint64 - f
					expectedRemainingAttempts := float64(remainingResults) * expectedAttemptsPerResult
					etaSeconds := expectedRemainingAttempts / rate

					switch {
					case etaSeconds < 60:
						etaStr = fmt.Sprintf(" | ETA: %.0fs", etaSeconds)
					case etaSeconds < 3600:
						etaStr = fmt.Sprintf(" | ETA: %.1fm", etaSeconds/60)
					default:
						etaStr = fmt.Sprintf(" | ETA: %.1fh", etaSeconds/3600)
					}
				}
			}

			fmt.Printf("\r[Stats] Attempts: %d | Found: %d | Rate: %.0f/sec | Elapsed: %.1fs%s",
				a, f, rate, elapsed.Seconds(), etaStr)
		case <-done:
			fmt.Println() // New line after final stats
			return
		}
	}
}
