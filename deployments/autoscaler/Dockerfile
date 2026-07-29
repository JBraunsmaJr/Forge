FROM golang:1.26.5-alpine3.20 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o forge-autoscaler ./cmd/forge-autoscaler

FROM alpine:3.20.1
RUN apk add --no-cache ca-certificates=20241121-r1 docker-cli=26.1.4-r0
COPY --from=builder /app/forge-autoscaler /usr/local/bin/forge-autoscaler
ENTRYPOINT ["forge-autoscaler"]
