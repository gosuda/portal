//go:build js && wasm

package utils

import (
	"errors"
	"io"
	"syscall/js"
)

var (
	_Fetch   = js.Global().Get("fetch")
	_Promise = js.Global().Get("Promise")
)

// Fetch performs a simple HTTP GET request using the JS fetch API
func Fetch(url string) ([]byte, error) {
	if _Fetch.IsUndefined() {
		return nil, errors.New("fetch API not supported")
	}

	// Create a channel to receive the result
	resultCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	var bufSuccess, bufFailure js.Func

	// Call fetch
	promise := _Fetch.Invoke(url)

	// Handle response
	success := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		response := args[0]

		// Check status
		if !response.Get("ok").Bool() {
			errCh <- errors.New(response.Get("statusText").String())
			return nil
		}

		// Get arrayBuffer
		promise := response.Call("arrayBuffer")

		// Handle arrayBuffer
		bufSuccess = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			arrayBuffer := args[0]
			uint8Array := js.Global().Get("Uint8Array").New(arrayBuffer)

			dst := make([]byte, uint8Array.Get("length").Int())
			js.CopyBytesToGo(dst, uint8Array)

			resultCh <- dst
			return nil
		})

		bufFailure = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			errCh <- errors.New("failed to read body")
			return nil
		})

		promise.Call("then", bufSuccess).Call("catch", bufFailure)

		return nil
	})

	failure := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		errCh <- errors.New(args[0].Get("message").String())
		return nil
	})

	defer func() {
		success.Release()
		failure.Release()
		if bufSuccess.Truthy() {
			bufSuccess.Release()
		}
		if bufFailure.Truthy() {
			bufFailure.Release()
		}
	}()

	promise.Call("then", success).Call("catch", failure)

	select {
	case data := <-resultCh:
		return data, nil
	case err := <-errCh:
		return nil, err
	}
}

// HTTPClient interface for TinyGo compatibility if needed
type HTTPClient interface {
	Get(url string) (io.ReadCloser, error)
}
