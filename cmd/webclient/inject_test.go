//go:build js && wasm

package main

import (
	"bytes"
	"testing"
)

func TestInjectHTMLHead(t *testing.T) {
	input := []byte("<html><head><title>x</title></head><body></body></html>")
	out := InjectHTML(input)
	if !bytes.Contains(out, polyfillJS) {
		t.Fatalf("expected injected polyfill")
	}
	headIdx := bytes.Index(bytes.ToLower(out), []byte("</head>"))
	scriptIdx := bytes.Index(out, polyfillJS)
	if scriptIdx == -1 || headIdx == -1 || scriptIdx > headIdx {
		t.Fatalf("expected polyfill to be injected before </head>")
	}
}

func TestInjectHTMLBody(t *testing.T) {
	input := []byte("<html><body><div></div></body></html>")
	out := InjectHTML(input)
	if !bytes.Contains(out, polyfillJS) {
		t.Fatalf("expected injected polyfill")
	}
	bodyIdx := bytes.Index(bytes.ToLower(out), []byte("</body>"))
	scriptIdx := bytes.Index(out, polyfillJS)
	if scriptIdx == -1 || bodyIdx == -1 || scriptIdx > bodyIdx {
		t.Fatalf("expected polyfill to be injected before </body>")
	}
}

func TestInjectHTMLFallback(t *testing.T) {
	input := []byte("plain")
	out := InjectHTML(input)
	if !bytes.Contains(out, polyfillJS) {
		t.Fatalf("expected injected polyfill")
	}
	if !bytes.HasPrefix(out, input) {
		t.Fatalf("expected original content preserved")
	}
}
