FROM golang:alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o forge-autoscaler ./cmd/forge-autoscaler

FROM alpine:latest
RUN apk add --no-cache ca-certificates docker-cli
COPY --from=builder /app/forge-autoscaler /usr/local/bin/forge-autoscaler
ENTRYPOINT ["forge-autoscaler"]
