FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY *.go ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /fpga-compiler .

# Final stage
FROM alpine:3.23

# Install FPGA toolchain from edge repository
RUN apk add --no-cache \
    --repository=http://dl-cdn.alpinelinux.org/alpine/edge/testing \
    yosys \
    nextpnr-ice40 \
    icestorm

# Copy the binary from builder
COPY --from=builder /fpga-compiler /usr/local/bin/fpga-compiler

# Create app directory for verilog files
RUN mkdir -p /app/verilog
WORKDIR /app

# Copy verilog files (these will be copied from the host)
COPY verilog/ /app/verilog/

# Expose port
EXPOSE 8080

# Run the application
CMD ["fpga-compiler"]
