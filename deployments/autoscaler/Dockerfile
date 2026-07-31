FROM golang:1.26.5-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o forge-autoscaler ./cmd/forge-autoscaler

FROM alpine:3.20.1
RUN apk add --no-cache ca-certificates docker-cli
COPY --from=builder /app/forge-autoscaler /usr/local/bin/forge-autoscaler
ENTRYPOINT ["forge-autoscaler"]
