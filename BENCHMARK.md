# Benchmarking

This document describes the benchmarking setup for the `portal` project.

## Portal Benchmark

The benchmarks for the `portal` package are located in `portal/benchmark_test.go`. These benchmarks are designed to measure the performance of the core relay functionality.

### Running the Benchmarks

A `Makefile` target is provided to simplify running the benchmarks and generating a report.

```sh
make bench-portal
```

This command will:

1.  Run the benchmarks for the `portal` package using `go test`.
2.  Enable CPU and memory profiling.
3.  Generate a summary report in `bench/results/YYYY-MM-DD-bench HH:MM:SS UTC-portal.md`.

## Benchmark Reporter

The report generation is handled by a small Go program in `cmd/bench-reporter`. This program parses the output of `go test -bench` and the profiling data to create a human-readable markdown report.

## Viewing Reports

A web server is provided to view the generated benchmark reports in a user-friendly HTML format.

To start the server, run:

```sh
make run-report-server
```

Then, open your web browser and navigate to `http://localhost:8081` to see a list of available reports.
