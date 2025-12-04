# Tiny Tapeout FPGA Compilation Server

A lightweight Go server that compiles Verilog projects using Yosys and nextpnr-ice40, streaming output in real-time.

## Features

- Receives Verilog source files via HTTP POST
- Compiles using Yosys + nextpnr-ice40 + icepack
- Streams compilation output in real-time using Server-Sent Events (SSE)
- Returns the compiled bitstream (.bin file) on success

## Quick Start

**Start the server:**
```bash
docker-compose up --build
```

**Check health:**
```bash
curl http://localhost:8080/health
```

**Test the API:**
```bash
make test-api
```

You should see streaming output from yosys, nextpnr, and icepack!

**Stop the server:**
```bash
docker-compose down
```

## Building

```bash
docker build -t fpga-compiler .
```

Or using docker-compose:

```bash
docker-compose build
```

## Running

```bash
docker run -p 8080:8080 fpga-compiler
```

Or using docker-compose:

```bash
docker-compose up
```

## API

### POST /api/compile

Compiles Verilog files and returns the bitstream.

**Request Body:**

```json
{
  "sources": {
    "project.v": "module project_top(...); ... endmodule",
    "other.v": "module other(...); ... endmodule"
  },
  "topModule": "tt_um_project_name"
}
```

**Response:**

Server-Sent Events stream with JSON messages:

```json
{"type": "command", "command": "yosys", "args": ["..."]}
{"type": "stdout", "data": "Output text..."}
{"type": "stderr", "data": "Error text..."}
{"type": "success", "data": "base64:...bitstream data..."}
{"type": "error", "message": "Error message"}
```

### GET /health

Health check endpoint.

## Environment Variables

- `PORT` - Server port (default: 8080)

## Development

```bash
# Install dependencies
go mod download

# Run locally (requires yosys, nextpnr-ice40, icepack installed)
go run main.go
```
