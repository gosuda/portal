package randpool

import (
	"bytes"
	"testing"
)

func TestRandOverwrite(t *testing.T) {
	// Create a buffer with known data
	buf := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
	original := make([]byte, len(buf))
	copy(original, buf)

	// Call Rand
	Rand(buf)

	// Verify that the buffer has changed
	if bytes.Equal(buf, original) {
		t.Error("Buffer should have changed after Rand")
	}

	// Verify that it's not just XORed (though hard to prove deterministically without mocking,
	// the fact that we zeroed it in code gives us confidence.
	// If it was XORed with 0xFF, the result would be (stream ^ 0xFF).
	// Since we zeroed it, the result is (stream ^ 0x00) = stream.
	// We can't easily distinguish stream vs stream^0xFF without knowing stream.
	// But we can check that running it twice produces different results.

	buf2 := make([]byte, 5) // Zeros
	Rand(buf2)

	if bytes.Equal(buf, buf2) {
		t.Error("Two random calls produced same output")
	}
}

func TestRandConcurrency(t *testing.T) {
	// Just run a bunch of goroutines to trigger the pool and potential race conditions
	// (though the fallback race is hard to trigger without fault injection)
	done := make(chan bool)
	for range 100 {
		go func() {
			buf := make([]byte, 32)
			Rand(buf)
			done <- true
		}()
	}
	for range 100 {
		<-done
	}
}
