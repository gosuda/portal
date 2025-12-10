//go:build !js

package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
)

const (
	port         = "8082"
	wasmServeDir = "cmd/wasm-bench-server/dist/wasm"
)

// wasmMiddleware sets the correct Content-Type for .wasm files.
func wasmMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".wasm.br") {
			w.Header().Set("Content-Type", "application/wasm")
			w.Header().Set("Content-Encoding", "br")
			// Also set immutable cache headers for content-addressed files
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if strings.HasSuffix(r.URL.Path, ".wasm") {
			w.Header().Set("Content-Type", "application/wasm")
			// Also set immutable cache headers for content-addressed files
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		next.ServeHTTP(w, r)
	})
}

// wasmFileHandler scans the wasm directory and returns the content-addressed wasm filename.
func wasmFileHandler(w http.ResponseWriter, r *http.Request) {
	files, err := ioutil.ReadDir(wasmServeDir)
	if err != nil {
		log.Printf("Error reading wasm directory: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	for _, file := range files {
		// The build process creates a .wasm.br file
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".wasm.br") {
			wasmFile := file.Name()
			response := map[string]string{"fileName": wasmFile}

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			json.NewEncoder(w).Encode(response)
			return
		}
	}

	log.Println("WASM file not found in wasm directory")
	http.NotFound(w, r)
}

func main() {
	// API endpoint to get the dynamic wasm filename
	http.HandleFunc("/api/wasm-file", wasmFileHandler)

	// Serve the main wasm executable from the dist/wasm directory
	fs := http.FileServer(http.Dir(wasmServeDir))
	http.Handle("/wasm/", http.StripPrefix("/wasm/", wasmMiddleware(fs)))
	// Serve the static files for the benchmark page
	http.Handle("/", http.FileServer(http.Dir("cmd/wasm-bench-server/static")))

	log.Printf("Starting WASM benchmark server on port %s...", port)
	log.Printf("Open http://localhost:%s/ to run the benchmark.", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}
