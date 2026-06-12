# Multi-stage build for FastRG Controller
# Stage 1: Build frontend
FROM node:24-alpine AS frontend-builder

WORKDIR /app/web

# Copy package files
COPY web/package.json web/package-lock.json ./

# Install dependencies
RUN npm ci

# Copy frontend source
COPY web/ ./

# Build frontend
RUN npm run build

# Stage 2: Build backend
FROM golang:alpine AS backend-builder

WORKDIR /app

# Install dependencies (including curl for downloading proto files)
RUN apk add --no-cache git protobuf protobuf-dev curl

# Install Go protobuf tools
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest && \
    go install github.com/swaggo/swag/cmd/swag@latest

# Copy go mod files
COPY go.mod go.sum ./

RUN go get -u github.com/swaggo/gin-swagger && go get -u github.com/swaggo/files

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Clean Go cache to force rebuild with latest embedded files
RUN go clean -cache

# Generate protobuf code
RUN cd proto && go generate ./... || true

# Generate OpenAPI code
RUN swag init --parseDependency --parseInternal

# Build backend
RUN CGO_ENABLED=0 GOOS=linux go build -o bin/controller .

# Stage 3: Final runtime image
FROM ubuntu:24.04

# Install necessary packages
RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    openssl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy built backend
COPY --from=backend-builder /app/bin/controller ./

# Copy built frontend
COPY --from=frontend-builder /app/web/build ./web/build

# Copy any necessary configuration files
COPY proto/ ./proto/

# Create directories for certificates and data
RUN mkdir -p certs logs

# Generate self-signed certificates
RUN openssl req -x509 -newkey rsa:4096 -keyout certs/server.key -out certs/server.crt \
    -days 365 -nodes -subj "/CN=localhost/O=FastRG Controller/C=TW" \
    -addext "subjectAltName=DNS:localhost,IP:127.0.0.1,IP:0.0.0.0" && \
    chmod 644 certs/server.key certs/server.crt

# Create non-root user and set permissions
#RUN groupadd -g 1001 fastrg && \
    #useradd -r -u 1001 -g fastrg -s /bin/bash fastrg && \
    #chown -R fastrg:fastrg /app

#USER fastrg
USER root

# Environment variables with defaults
ENV ETCD_ENDPOINTS=etcd:2379
ENV GRPC_PORT=50051
ENV HTTP_REDIRECT_PORT=8080
ENV HTTPS_PORT=8443

# Expose ports
EXPOSE 50051 8080 8443 8444 55688

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -k -f https://localhost:8443/api/health || exit 1

# Start the application
CMD ["./controller"]
