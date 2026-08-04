# --- Stage 1: Build UI ---
FROM node:22-alpine AS uibuilder
WORKDIR /app/ui
COPY ui/ .
RUN rm -rf /app/internal/scheduler/web/dist && npm install && npm run build

FROM golang:1.26.5-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Copy the built UI assets (either from the context or from the build above)
COPY --from=uibuilder /app/internal/scheduler/web/dist ./internal/scheduler/web/dist

RUN go build -o forge-autoscaler ./cmd/forge-autoscaler

FROM alpine:3.20.1
RUN apk add --no-cache ca-certificates docker-cli
COPY --from=builder /app/forge-autoscaler /usr/local/bin/forge-autoscaler
ENTRYPOINT ["forge-autoscaler"]
