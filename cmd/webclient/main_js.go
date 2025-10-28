package main

import "runtime"

func main() {
	if runtime.Compiler == "tinygo" || runtime.GOARCH != "wasm" {
		return
	}

	ch := make(chan struct{})
	<-ch
}
