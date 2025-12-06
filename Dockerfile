FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install git for go mod download
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum* ./

# Download dependencies
RUN go mod download || go mod tidy

# Copy source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -o chk-gsheet .

# Final stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/chk-gsheet .

EXPOSE 8002

CMD ["./chk-gsheet"]

