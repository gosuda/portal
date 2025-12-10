package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/russross/blackfriday/v2"
)

const (
	reportDir = "bench/results"
	port      = "8081"
)

func main() {
	http.HandleFunc("/reports/", reportHandler)
	http.HandleFunc("/", listHandler)

	log.Printf("Starting server on port %s...\n", port)
	log.Printf("Available reports at http://localhost:%s/\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}

func listHandler(w http.ResponseWriter, r *http.Request) {
    files, err := ioutil.ReadDir(reportDir)
    if err != nil {
        http.Error(w, fmt.Sprintf("Unable to read report directory: %v", err), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    fmt.Fprintln(w, "<h1>Available Benchmark Reports</h1>")
    fmt.Fprintln(w, "<ul>")
    for _, file := range files {
        if !file.IsDir() && strings.HasSuffix(file.Name(), ".md") {
            reportName := strings.TrimSuffix(file.Name(), ".md")
            fmt.Fprintf(w, `<li><a href="/reports/%s">%s</a></li>`, reportName, reportName)
        }
    }
    fmt.Fprintln(w, "</ul>")
}

func reportHandler(w http.ResponseWriter, r *http.Request) {
	reportName := strings.TrimPrefix(r.URL.Path, "/reports/")
	if reportName == "" {
		http.Error(w, "Please specify a report name.", http.StatusBadRequest)
		return
	}

	mdPath := filepath.Join(reportDir, reportName+".md")
	markdown, err := ioutil.ReadFile(mdPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Report not found: %s", reportName), http.StatusNotFound)
		return
	}

	html := blackfriday.Run(markdown)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write(html)
	if err != nil {
		log.Printf("Error writing response for %s: %v", reportName, err)
	}
}
