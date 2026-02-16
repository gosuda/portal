package randpool

import (
	"crypto/rand"
	"fmt"
	"io"
)

func Rand(dst []byte) {
	if len(dst) == 0 {
		return
	}
	if _, err := io.ReadFull(rand.Reader, dst); err != nil {
		panic(fmt.Errorf("randpool: failed to read crypto randomness: %w", err))
	}
}
