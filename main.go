// Tiny Tapeout FPGA Compilation Server
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// CompilationTimeout is the maximum time allowed for a compilation request
	CompilationTimeout = 120 * time.Second
)

type CompileRequest struct {
	Sources   map[string]string `json:"sources"`
	TopModule string            `json:"topModule"`
	Freq      *int              `json:"freq,omitempty"`
	Seed      *int              `json:"seed,omitempty"`
}

type StreamMessage struct {
	Type    string   `json:"type"` // "command", "stdout", "stderr", "error", "success"
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	Data    string   `json:"data,omitempty"`
	Message string   `json:"message,omitempty"`
}

var (
	compilationRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fpga_compilation_requests_total",
			Help: "Total number of FPGA compilation requests",
		},
		[]string{"status"}, // "success" or "error"
	)

	compilationDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "fpga_compilation_duration_seconds",
			Help:    "Duration of FPGA compilation requests in seconds",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1s, 2s, 4s, 8s, ... up to ~512s
		},
	)

	compilationInProgress = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "fpga_compilation_in_progress",
			Help: "Number of FPGA compilations currently in progress",
		},
	)

	commandExecutionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "fpga_command_execution_duration_seconds",
			Help:    "Duration of individual FPGA toolchain command execution in seconds",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10), // 0.1s, 0.2s, 0.4s, ... up to ~51s
		},
		[]string{"command"}, // "yosys", "nextpnr-ice40", "icepack"
	)
)

func init() {
	prometheus.MustRegister(compilationRequestsTotal)
	prometheus.MustRegister(compilationDuration)
	prometheus.MustRegister(compilationInProgress)
	prometheus.MustRegister(commandExecutionDuration)
}

func main() {
	http.HandleFunc("/api/compile", loggingMiddleware(corsMiddleware(handleCompile)))
	http.HandleFunc("/health", loggingMiddleware(handleHealth))
	http.Handle("/metrics", promhttp.Handler())

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newLoggingResponseWriter(w http.ResponseWriter) *loggingResponseWriter {
	return &loggingResponseWriter{w, http.StatusOK}
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Flush() {
	if f, ok := lrw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func loggingMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := newLoggingResponseWriter(w)

		next(lrw, r)

		duration := time.Since(start)
		log.Printf("%s %s - %d - %s - %v",
			r.Method,
			r.URL.Path,
			lrw.statusCode,
			r.RemoteAddr,
			duration,
		)
	}
}

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func handleCompile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CompileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(r.Context(), CompilationTimeout)
	defer cancel()

	// Start metrics tracking
	startTime := time.Now()
	compilationInProgress.Inc()
	status := "error" // Default to error, set to success on completion
	defer func() {
		compilationInProgress.Dec()
		compilationDuration.Observe(time.Since(startTime).Seconds())
		compilationRequestsTotal.WithLabelValues(status).Inc()
	}()

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create temporary directory for this compilation
	workDir := filepath.Join("/tmp", "fpga-compile-"+uuid.New().String())
	if err := os.MkdirAll(workDir, 0755); err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: "Failed to create work directory"})
		return
	}
	defer os.RemoveAll(workDir)

	// Write source files
	for name, content := range req.Sources {
		filePath := filepath.Join(workDir, name)
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			sendSSE(w, flusher, StreamMessage{Type: "error", Message: fmt.Sprintf("Failed to write file %s", name)})
			return
		}
	}

	// Load FPGA top verilog and PCF
	fpgaTopPath := "/app/verilog/tt_fpga_top.v"
	fpgaTopContent, err := os.ReadFile(fpgaTopPath)
	if err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: "Failed to read FPGA top verilog"})
		return
	}

	// Replace placeholder with actual top module
	topVerilog := strings.Replace(string(fpgaTopContent), "__tt_um_placeholder", req.TopModule, -1)
	if err := os.WriteFile(filepath.Join(workDir, "top.v"), []byte(topVerilog), 0644); err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: "Failed to write top.v"})
		return
	}

	// Copy PCF file
	pcfPath := "/app/verilog/tt_fpga_fabricfox.pcf"
	pcfContent, err := os.ReadFile(pcfPath)
	if err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: "Failed to read PCF file"})
		return
	}
	if err := os.WriteFile(filepath.Join(workDir, "fpga.pcf"), pcfContent, 0644); err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: "Failed to write fpga.pcf"})
		return
	}

	// Run Yosys
	sourceFiles := make([]string, 0, len(req.Sources))
	for name := range req.Sources {
		sourceFiles = append(sourceFiles, name)
	}

	yosysArgs := []string{
		"-l", "yosys.log",
		"-DSYNTH",
		"-p", "synth_ice40 -top tt_fpga_top -json output.json",
		"top.v",
	}
	yosysArgs = append(yosysArgs, sourceFiles...)

	if !runCommand(ctx, w, flusher, workDir, "yosys", yosysArgs) {
		return
	}

	// Run nextpnr-ice40
	freq := 12
	if req.Freq != nil {
		freq = *req.Freq
	}
	seed := 42
	if req.Seed != nil {
		seed = *req.Seed
	}

	nextpnrArgs := []string{
		"--pcf-allow-unconstrained",
		"--seed", fmt.Sprintf("%d", seed),
		"--freq", fmt.Sprintf("%d", freq),
		"--package", "sg48",
		"--up5k",
		"--asc", "output.asc",
		"--pcf", "fpga.pcf",
		"--json", "output.json",
	}

	if !runCommand(ctx, w, flusher, workDir, "nextpnr-ice40", nextpnrArgs) {
		return
	}

	// Run icepack
	icepackArgs := []string{"output.asc", "output.bin"}
	if !runCommand(ctx, w, flusher, workDir, "icepack", icepackArgs) {
		return
	}

	// Read and send the bitstream
	bitstreamPath := filepath.Join(workDir, "output.bin")
	bitstream, err := os.ReadFile(bitstreamPath)
	if err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: "Failed to read bitstream"})
		return
	}

	// Send success message with bitstream as base64
	status = "success"
	sendSSE(w, flusher, StreamMessage{
		Type: "success",
		Data: fmt.Sprintf("base64:%s", base64.StdEncoding.EncodeToString(bitstream)),
	})
}

func runCommand(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, workDir, command string, args []string) bool {
	startTime := time.Now()
	defer func() {
		commandExecutionDuration.WithLabelValues(command).Observe(time.Since(startTime).Seconds())
	}()

	sendSSE(w, flusher, StreamMessage{
		Type:    "command",
		Command: command,
		Args:    args,
	})

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: fmt.Sprintf("Failed to create stdout pipe: %v", err)})
		return false
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: fmt.Sprintf("Failed to create stderr pipe: %v", err)})
		return false
	}

	if err := cmd.Start(); err != nil {
		sendSSE(w, flusher, StreamMessage{Type: "error", Message: fmt.Sprintf("Failed to start %s: %v", command, err)})
		return false
	}

	// Stream stdout
	go streamOutput(w, flusher, stdout, "stdout")

	// Stream stderr
	go streamOutput(w, flusher, stderr, "stderr")

	if err := cmd.Wait(); err != nil {
		// Check if the error is due to context timeout
		if ctx.Err() == context.DeadlineExceeded {
			sendSSE(w, flusher, StreamMessage{Type: "error", Message: fmt.Sprintf("Compilation timeout: operation exceeded %v", CompilationTimeout)})
		} else {
			sendSSE(w, flusher, StreamMessage{Type: "error", Message: fmt.Sprintf("%s failed: %v", command, err)})
		}
		return false
	}

	return true
}

func streamOutput(w http.ResponseWriter, flusher http.Flusher, reader io.Reader, stream string) {
	buf := make([]byte, 1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			sendSSE(w, flusher, StreamMessage{
				Type: stream,
				Data: string(buf[:n]),
			})
		}
		if err != nil {
			break
		}
	}
}

func sendSSE(w http.ResponseWriter, flusher http.Flusher, msg StreamMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return
	}

	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}
