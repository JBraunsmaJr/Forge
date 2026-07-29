package autoscaler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/provisioner"
)

type Config struct {
	HotPoolSize  int
	MaxBurstSize int
	IdleTimeout  time.Duration
	ScaleUpDelay time.Duration // Cooldown
	PollInterval time.Duration
	SchedulerURL string
	APIToken     string
}

type Autoscaler struct {
	cfg         Config
	provisioner provisioner.CloudProvisioner
	client      *http.Client

	idleTimers  map[provisioner.InstanceID]time.Time
	tearingDown map[provisioner.InstanceID]bool
	lastScaleUp time.Time
	mu          sync.Mutex
}

func New(cfg Config, prov provisioner.CloudProvisioner) *Autoscaler {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Second
	}
	if cfg.ScaleUpDelay == 0 {
		cfg.ScaleUpDelay = 1 * time.Minute
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 5 * time.Minute
	}

	return &Autoscaler{
		cfg:         cfg,
		provisioner: prov,
		client:      &http.Client{Timeout: 10 * time.Second},
		idleTimers:  make(map[provisioner.InstanceID]time.Time),
		tearingDown: make(map[provisioner.InstanceID]bool),
	}
}

func (a *Autoscaler) Run(ctx context.Context) error {
	log.Printf("[autoscaler] starting control loop (hot: %d, burst: %d, idle: %v)", a.cfg.HotPoolSize, a.cfg.MaxBurstSize, a.cfg.IdleTimeout)
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := a.tick(ctx); err != nil {
				log.Printf("[autoscaler] tick error: %v", err)
			}
		}
	}
}

func (a *Autoscaler) tick(ctx context.Context) error {
	// 1. Get current state from Cloud
	instances, err := a.provisioner.ListInstances(ctx)
	if err != nil {
		provisionerErrors.WithLabelValues("list").Inc()
		return fmt.Errorf("list instances: %w", err)
	}

	// 2. Get current state from Scheduler
	agents, err := a.listAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	queueDepth, err := a.getQueueDepth(ctx)
	if err != nil {
		return fmt.Errorf("get queue depth: %w", err)
	}
	observedQueueDepth.Set(float64(queueDepth))

	maxPoolSize.WithLabelValues("hot").Set(float64(a.cfg.HotPoolSize))
	maxPoolSize.WithLabelValues("burst").Set(float64(a.cfg.MaxBurstSize))

	// 3. Hot-pool floor enforcement
	hotInstances := 0
	burstInstances := 0
	for _, inst := range instances {
		if inst.Pool == "hot" {
			hotInstances++
		} else if inst.Pool == "burst" {
			burstInstances++
		}
	}
	poolSize.WithLabelValues("hot").Set(float64(hotInstances))
	poolSize.WithLabelValues("burst").Set(float64(burstInstances))

	if hotInstances < a.cfg.HotPoolSize {
		needed := a.cfg.HotPoolSize - hotInstances
		log.Printf("[autoscaler] hot pool under capacity: %d/%d, scaling up...", hotInstances, a.cfg.HotPoolSize)
		_, err := a.provisioner.ScaleUp(ctx, "hot", needed, nil)
		if err != nil {
			provisionerErrors.WithLabelValues("scale_up_hot").Inc()
			log.Printf("[autoscaler] failed to scale up hot pool: %v", err)
		} else {
			scaleEvents.WithLabelValues("hot", "up").Add(float64(needed))
		}
	}

	// 4. Burst scale-up trigger
	totalCapacity := 0
	for _, agent := range agents {
		if agent.Connected && !agent.Draining {
			totalCapacity += agent.Concurrency
		}
	}

	busyCapacity := 0
	for _, agent := range agents {
		if agent.Connected {
			busyCapacity += agent.ActiveJobsCount
		}
	}

	availableCapacity := totalCapacity - busyCapacity
	if availableCapacity < 0 {
		availableCapacity = 0
	}

	if queueDepth > availableCapacity && burstInstances < a.cfg.MaxBurstSize {
		if time.Since(a.lastScaleUp) > a.cfg.ScaleUpDelay {
			needed := queueDepth - availableCapacity
			// For simplicity, we scale up by at least 1 if needed
			if needed <= 0 {
				needed = 1
			}
			if needed > a.cfg.MaxBurstSize-burstInstances {
				needed = a.cfg.MaxBurstSize - burstInstances
			}

			if needed > 0 {
				log.Printf("[autoscaler] queue depth %d > available capacity %d, scaling up burst pool by %d...", queueDepth, availableCapacity, needed)
				_, err := a.provisioner.ScaleUp(ctx, "burst", needed, nil)
				if err != nil {
					provisionerErrors.WithLabelValues("scale_up_burst").Inc()
					log.Printf("[autoscaler] failed to scale up burst pool: %v", err)
				} else {
					a.lastScaleUp = time.Now()
					scaleEvents.WithLabelValues("burst", "up").Add(float64(needed))
				}
			}
		} else {
			// Optional: log throttling periodically
		}
	}

	// 5. Idle tracking and Scale-down (Burst only)
	a.mu.Lock()
	activeBurstIDs := make(map[provisioner.InstanceID]bool)
	for _, inst := range instances {
		if inst.Pool != "burst" {
			continue
		}
		activeBurstIDs[inst.ID] = true

		// Find corresponding agent
		var agent *api.AgentInfo
		for i := range agents {
			// Use exact match for canonical ID
			if agents[i].ID == string(inst.ID) {
				agent = &agents[i]
				break
			}
		}

		if agent == nil {
			// Agent not registered yet, but we shouldn't kill it unless it's very old
			if time.Since(inst.CreatedAt) > 10*time.Minute {
				log.Printf("[autoscaler] burst instance %s never registered, tearing down...", inst.ID)
				if err := a.provisioner.ScaleDown(ctx, []provisioner.InstanceID{inst.ID}); err != nil {
					provisionerErrors.WithLabelValues("scale_down_orphan").Inc()
					log.Printf("[autoscaler] failed to scale down orphan instance %s: %v", inst.ID, err)
				}
			}
			continue
		}

		if agent.ActiveJobsCount == 0 {
			if _, ok := a.idleTimers[inst.ID]; !ok {
				a.idleTimers[inst.ID] = time.Now()
			} else if time.Since(a.idleTimers[inst.ID]) > a.cfg.IdleTimeout && !a.tearingDown[inst.ID] {
				log.Printf("[autoscaler] burst instance %s idle for %v, tearing down...", inst.ID, time.Since(a.idleTimers[inst.ID]).Round(time.Second))
				a.tearingDown[inst.ID] = true
				go a.teardown(ctx, inst.ID)
				delete(a.idleTimers, inst.ID)
			}
		} else {
			delete(a.idleTimers, inst.ID)
		}
	}

	// Cleanup idle timers for instances that are gone
	for id := range a.idleTimers {
		if !activeBurstIDs[id] {
			delete(a.idleTimers, id)
		}
	}
	a.mu.Unlock()

	return nil
}

func (a *Autoscaler) teardown(ctx context.Context, id provisioner.InstanceID) {
	defer func() {
		a.mu.Lock()
		delete(a.tearingDown, id)
		a.mu.Unlock()
	}()

	// Drain
	if err := a.drainAgent(ctx, string(id)); err != nil {
		log.Printf("[autoscaler] failed to drain agent %s: %v", id, err)
	}

	// Bounded wait for ActiveJobsCount == 0
	start := time.Now()
Loop:
	for time.Since(start) < 5*time.Minute {
		select {
		case <-ctx.Done():
			break Loop
		default:
		}

		agents, err := a.listAgents(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				break Loop
			case <-time.After(5 * time.Second):
			}
			continue
		}

		var agent *api.AgentInfo
		for i := range agents {
			if agents[i].ID == string(id) {
				agent = &agents[i]
				break
			}
		}

		if agent == nil || agent.ActiveJobsCount == 0 {
			break
		}

		select {
		case <-ctx.Done():
			break Loop
		case <-time.After(5 * time.Second):
		}
	}

	if err := a.provisioner.ScaleDown(ctx, []provisioner.InstanceID{id}); err != nil {
		provisionerErrors.WithLabelValues("scale_down").Inc()
		log.Printf("[autoscaler] failed to scale down instance %s: %v", id, err)
	} else {
		scaleEvents.WithLabelValues("burst", "down").Inc()
	}
}

func (a *Autoscaler) listAgents(ctx context.Context) ([]api.AgentInfo, error) {
	url := strings.TrimSuffix(a.cfg.SchedulerURL, "/") + "/api/v1/agents"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if a.cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list agents: unexpected status: %d", resp.StatusCode)
	}
	var agents []api.AgentInfo
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func (a *Autoscaler) getQueueDepth(ctx context.Context) (int, error) {
	url := strings.TrimSuffix(a.cfg.SchedulerURL, "/") + "/api/v1/queue/depth"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if a.cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("queue depth: unexpected status: %d", resp.StatusCode)
	}
	var res struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}
	return res.Count, nil
}

func (a *Autoscaler) drainAgent(ctx context.Context, id string) error {
	url := strings.TrimSuffix(a.cfg.SchedulerURL, "/") + "/api/v1/agents/" + id + "/drain"
	req, _ := http.NewRequestWithContext(ctx, "POST", url, nil)
	if a.cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.APIToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("drain agent: unexpected status: %d", resp.StatusCode)
	}
	return nil
}
