package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	reportDir = "bench/results"
)

// Fixed: Use \S+ because benchmark names can contain characters like '-' or '/'.
// Capture group index explanation:
// 1: Name
// 2: Runs
// 3: ns/op
// 4: ( ... MB/s)? -> Whole optional group (ignored)
// 5: MB/s numeric value
// 6: B/op
// 7: allocs/op
var benchRegex = regexp.MustCompile(`^(Benchmark\S+)\s+([0-9]+)\s+([0-9.]+) ns/op(\s+([0-9.]+) MB/s)?\s+([0-9.]+) B/op\s+([0-9.]+) allocs/op$`)

type BenchmarkResult struct {
	Name        string
	Runs        string
	NanosPerOp  string
	MBPerSec    string
	BPerOp      string
	AllocsPerOp string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Read data from Stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: %w", err)
	}

	results, err := parseBenchmarkOutput(string(input))
	if err != nil {
		return fmt.Errorf("failed to parse benchmark output: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("No benchmark results found or parsed correctly.")
		return nil
	}

	return generateReport(results)
}

func parseBenchmarkOutput(input string) ([]BenchmarkResult, error) {
	var results []BenchmarkResult
	scanner := bufio.NewScanner(strings.NewReader(input))
	for scanner.Scan() {
		line := scanner.Text()
		matches := benchRegex.FindStringSubmatch(line)

		// matches length is fixed based on the number of regex groups (if matched)
		if len(matches) > 7 {
			mbps := "N/A"
			// If group 5 (MB/s number) is not empty, use it
			if matches[5] != "" {
				mbps = matches[5]
			}

			results = append(results, BenchmarkResult{
				Name:        matches[1],
				Runs:        matches[2],
				NanosPerOp:  matches[3],
				MBPerSec:    mbps,
				BPerOp:      matches[6], // Fixed: Adjusted index 5->6
				AllocsPerOp: matches[7], // Fixed: Adjusted index 6->7
			})
		}
	}
	return results, scanner.Err()
}

func generateReport(results []BenchmarkResult) error {
	today := time.Now().Format("2006-01-02 15:04:05 UTC")
	fileName := fmt.Sprintf("%s-bench-portal.md", today)

	// Fixed: Create directory if it does not exist
	if err := os.MkdirAll(reportDir, 0755); err != nil {
		return fmt.Errorf("failed to create report directory: %w", err)
	}

	filePath := filepath.Join(reportDir, fileName)

	f, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("could not create report file: %w", err)
	}
	defer f.Close()

	var b strings.Builder

	b.WriteString(fmt.Sprintf("# Portal Benchmark Report - %s\n\n", today))
	b.WriteString("## Benchmark Summary\n\n")
	b.WriteString("| Benchmark Name | Iterations | ns/op (lower is better) | MB/s (higher is better) | B/op (lower is better) | allocs/op (lower is better) |\n")
	b.WriteString("|---|---|---|---|---|---|\n") // Fixed: Corrected newline character position

	for _, res := range results {
		b.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s |\n", res.Name, res.Runs, res.NanosPerOp, res.MBPerSec, res.BPerOp, res.AllocsPerOp))
	}

	// Add template text
	b.WriteString("\n" + `## Performance Analysis

	### CPU Usage

	To analyze CPU usage, run the following command:

	` + "```" + `sh
	go tool pprof cpu.prof
	` + "```" + `

	*TODO: Add automated analysis of top CPU consuming functions here.*

	### Memory Usage

	To analyze memory allocation, run the following command:

	` + "```" + `sh
	go tool pprof mem.prof
	` + "```" + `

	*TODO: Add automated analysis of top memory allocating functions here.*

	### Bottlenecks & Spikes

	*TODO: This section would contain analysis of detected performance bottlenecks or significant spikes in resource usage during the benchmark run. This requires more sophisticated analysis of the pprof data.*

	### WASM Performance

	As requested, a separate web server for WASM benchmarking will be implemented. This server will provide a browser-based environment to measure performance and report the results as an HTML page.

	See ` + `the ` + "`BENCHMARK.md`" + ` file for more details.
	`)

	_, err = f.WriteString(b.String())
	if err != nil {
		return fmt.Errorf("failed to write report to file: %w", err)
	}

	fmt.Printf("Benchmark report generated at: %s\n", filePath)
	return nil
}
