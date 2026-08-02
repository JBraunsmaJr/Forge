package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"net/http"

	"github.com/JBraunsmaJr/forge/internal/autoscaler"
	"github.com/JBraunsmaJr/forge/internal/provisioner"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	schedulerURL := os.Getenv("FORGE_SCHEDULER_URL")
	if schedulerURL == "" {
		schedulerURL = "http://localhost:8080"
	}
	apiToken := os.Getenv("FORGE_API_TOKEN")

	hotPoolSize, _ := strconv.Atoi(os.Getenv("FORGE_AUTOSCALER_HOT_POOL_SIZE"))
	maxBurstSize, _ := strconv.Atoi(os.Getenv("FORGE_AUTOSCALER_MAX_BURST_SIZE"))
	if maxBurstSize == 0 {
		maxBurstSize = 10
	}

	idleTimeoutStr := os.Getenv("FORGE_AUTOSCALER_IDLE_TIMEOUT")
	idleTimeout := 5 * time.Minute
	if val, err := time.ParseDuration(idleTimeoutStr); err == nil {
		idleTimeout = val
	}

	pollIntervalStr := os.Getenv("FORGE_AUTOSCALER_POLL_INTERVAL")
	pollInterval := 10 * time.Second
	if val, err := time.ParseDuration(pollIntervalStr); err == nil {
		pollInterval = val
	}

	scaleUpDelayStr := os.Getenv("FORGE_AUTOSCALER_SCALE_UP_DELAY")
	scaleUpDelay := 1 * time.Minute
	if val, err := time.ParseDuration(scaleUpDelayStr); err == nil {
		scaleUpDelay = val
	}

	providerType := os.Getenv("FORGE_AUTOSCALER_PROVIDER")
	var prov provisioner.CloudProvisioner

	switch providerType {
	case "azure":
		subID := os.Getenv("FORGE_AZURE_SUBSCRIPTION_ID")
		rg := os.Getenv("FORGE_AZURE_RESOURCE_GROUP")
		hot := os.Getenv("FORGE_AZURE_HOT_VMSS")
		burst := os.Getenv("FORGE_AZURE_BURST_VMSS")
		var err error
		prov, err = provisioner.NewAzureVMSSProvisioner(subID, rg, hot, burst)
		if err != nil {
			log.Fatalf("[autoscaler] failed to initialize azure provisioner: %v", err)
		}
	default:
		log.Printf("[autoscaler] using docker-fake provider")
		image := os.Getenv("FORGE_AUTOSCALER_DOCKER_IMAGE")
		if image == "" {
			image = "ghcr.io/jbraunsmajr/forge/forge:latest"
		}
		network := os.Getenv("FORGE_AUTOSCALER_DOCKER_NETWORK")
		agentID := os.Getenv("FORGE_PROXY_AGENT_ID")
		proxyURL := os.Getenv("FORGE_PROXY_URL")
		socketsVolume := os.Getenv("FORGE_AUTOSCALER_DOCKER_SOCKETS_VOLUME")
		if apiToken == "" {
			log.Printf("[autoscaler] WARNING: FORGE_API_TOKEN is not set — agents spawned by the docker-fake provider will get no token and every scheduler call they make (checkout, log/status reporting, etc.) will fail with 401")
		}
		if proxyURL == "" || socketsVolume == "" {
			log.Printf("[autoscaler] WARNING: FORGE_PROXY_URL and/or FORGE_AUTOSCALER_DOCKER_SOCKETS_VOLUME is not set — agents spawned by the docker-fake provider will have no way to reach Docker, and pipeline steps using it will fail with \"is the docker daemon running?\"")
		}
		prov = &provisioner.DockerFakeProvisioner{
			Image:         image,
			SchedulerURL:  schedulerURL,
			Network:       network,
			AgentID:       agentID,
			APIToken:      apiToken,
			ProxyURL:      proxyURL,
			SocketsVolume: socketsVolume,
		}
	}

	cfg := autoscaler.Config{
		HotPoolSize:  hotPoolSize,
		MaxBurstSize: maxBurstSize,
		IdleTimeout:  idleTimeout,
		PollInterval: pollInterval,
		ScaleUpDelay: scaleUpDelay,
		SchedulerURL: schedulerURL,
		APIToken:     apiToken,
	}

	as := autoscaler.New(cfg, prov)

	// Start metrics server
	metricsPort := os.Getenv("FORGE_AUTOSCALER_METRICS_PORT")
	if metricsPort == "" {
		metricsPort = "9091"
	}
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		log.Printf("[autoscaler] metrics server starting on :%s", metricsPort)
		if err := http.ListenAndServe(":"+metricsPort, mux); err != nil {
			log.Printf("[autoscaler] metrics server error: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("\n[autoscaler] received shutdown signal, exiting...")
		cancel()
	}()

	if err := as.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("[autoscaler] fatal error: %v", err)
	}
}
