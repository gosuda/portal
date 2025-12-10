//go:build js && wasm

package main

import (
	"syscall/js"
	"time"

	"gosuda.org/portal/portal/core/cryptoops"
)

func main() {
	// Expose benchmark function
	js.Global().Set("benchmarkCrypto", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		iterations := 100
		if len(args) > 0 && args[0].Type() == js.TypeNumber {
			iterations = args[0].Int()
		}

		startTime := time.Now()
		for i := 0; i < iterations; i++ {
			_, err := cryptoops.NewCredential()
			if err != nil {
				// In a real app, you'd handle this error more gracefully
				// For the benchmark, we'll just log it to the console
				js.Global().Get("console").Call("error", "Failed to create credential during benchmark:", err.Error())
				return nil
			}
		}
		duration := time.Since(startTime)

		return duration.Milliseconds()
	}))

	// Signal to JS that the WASM module is ready
	js.Global().Call("wasmIsReady")

	// Prevent the Go program from exiting, allowing JS to call the exported function.
	select {}
}
