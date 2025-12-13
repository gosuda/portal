//go:build js && wasm

package utils

import (
	"errors"
	"io"
	"syscall/js"
)

var (
	ErrFailedToDial = errors.New("failed to dial websocket")
	ErrClosed       = errors.New("websocket connection closed")
)

var (
	_WebSocket   = js.Global().Get("WebSocket")
	_ArrayBuffer = js.Global().Get("ArrayBuffer")
	_Uint8Array  = js.Global().Get("Uint8Array")
)

type WsConn struct {
	ws js.Value

	messageChan chan []byte
	closeChan   chan struct{}

	funcsToBeReleased []js.Func
}

func (conn *WsConn) freeFuncs() {
	for _, f := range conn.funcsToBeReleased {
		f.Release()
	}
}

func DialWebSocket(uri string) (*WsConn, error) {
	errCh := make(chan error, 1)

	if _WebSocket.IsUndefined() {
		return nil, errors.New("WebSocket not supported in this environment")
	}

	ws := _WebSocket.New(uri)
	ws.Set("binaryType", "arraybuffer")

	conn := &WsConn{
		ws:          ws,
		messageChan: make(chan []byte, 32),
		closeChan:   make(chan struct{}, 1),
	}

	onOpen := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		errCh <- nil
		return nil
	})

	onError := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		errCh <- ErrFailedToDial
		return nil
	})

	onMessage := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		jsData := args[0].Get("data")
		if jsData.Type() == js.TypeString {
			// text frame
			data := []byte(jsData.String())

			conn.messageChan <- data
		} else if jsData.InstanceOf(_ArrayBuffer) {
			// binary frame
			array := _Uint8Array.New(jsData)
			byteLength := array.Get("byteLength").Int()
			data := make([]byte, byteLength)
			js.CopyBytesToGo(data, array)

			conn.messageChan <- data
		}

		return nil
	})

	onClose := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		close(conn.closeChan)
		return nil
	})

	conn.funcsToBeReleased = append(conn.funcsToBeReleased, onOpen, onError, onMessage, onClose)

	conn.ws.Call("addEventListener", "open", onOpen)
	conn.ws.Call("addEventListener", "error", onError)
	conn.ws.Call("addEventListener", "message", onMessage)
	conn.ws.Call("addEventListener", "close", onClose)

	select {
	case err := <-errCh:
		if err != nil {
			conn.freeFuncs()
			return nil, err
		}
	}

	return conn, nil
}

func (conn *WsConn) Close() error {
	conn.ws.Call("close")
	// Do not wait for onClose here to avoid deadlocks if called from JS callback
	// Let the event listener handle channel closing
	conn.freeFuncs()
	return nil
}

func (conn *WsConn) NextMessage() ([]byte, error) {
	select {
	case msg := <-conn.messageChan:
		return msg, nil
	case <-conn.closeChan:
		return nil, io.EOF // Use io.EOF for closed connection
	}
}

func (conn *WsConn) Send(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	// Copy data to JS Uint8Array
	// Note: We might optimize this by using a pool of JS arrays if allocation is high
	buffer := _ArrayBuffer.New(len(data))
	array := _Uint8Array.New(buffer)
	js.CopyBytesToJS(array, data)

	conn.ws.Call("send", buffer)
	return nil
}
