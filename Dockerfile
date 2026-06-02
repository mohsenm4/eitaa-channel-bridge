# syntax=docker/dockerfile:1.7

FROM golang:1.26-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/bridge ./cmd/bridge


FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /app/data /app/site

COPY --from=builder /out/bridge /usr/local/bin/bridge

WORKDIR /app

ENV EITAA_BRIDGE_STORAGE_SEEN_FILE=/app/data/seen.json \
    EITAA_BRIDGE_STORAGE_ARCHIVE_FILE=/app/data/messages.jsonl \
    EITAA_BRIDGE_TARGET_HTML_OUTPUT_DIR=/app/site

VOLUME ["/app/data", "/app/site"]

ENTRYPOINT ["/usr/local/bin/bridge"]
CMD ["run"]
