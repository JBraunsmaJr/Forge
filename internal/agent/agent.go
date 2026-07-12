package agent

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/cache"
	"github.com/JBraunsmaJr/forge/internal/compiler"
	"github.com/JBraunsmaJr/forge/internal/executor"
	"github.com/JBraunsmaJr/forge/internal/pb"
	"github.com/JBraunsmaJr/forge/internal/pipeline"
	"github.com/JBraunsmaJr/forge/internal/secrets"
	"github.com/JBraunsmaJr/forge/internal/tracing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func init() {
	mime.AddExtensionType(".html", "text/html")
	mime.AddExtensionType(".pdf", "application/pdf")
	mime.AddExtensionType(".txt", "text/plain")
	mime.AddExtensionType(".log", "text/plain")
	mime.AddExtensionType(".png", "image/png")
	mime.AddExtensionType(".jpg", "image/jpeg")
	mime.AddExtensionType(".jpeg", "image/jpeg")
}

const (
	pollInterval      = 2 * time.Second
	heartbeatInterval = 10 * time.Second
)

// Agent polls a Forge scheduler and executes jobs.
type Agent struct {
	id           string
	schedulerURL string
	workspaceDir string
	cacheDir     string
	logDir       string
	vault        *secrets.Client
	client       *http.Client
	apiToken     string // FORGE_API_TOKEN — sent with every scheduler request
	debugConts   sync.Map

	// Concurrency control
	maxConcurrency int
	semaphore      chan struct{}

	// gRPC communication
	grpcClient pb.AgentServiceClient
	out        chan *pb.AgentMessage

	// Cleanup configuration
	maxDockerGB      float64
	maxDockerPercent float64
	pruneSchedule    string
	activeJobs       sync.Map // map[string]context.CancelFunc
	cleanupMu        sync.Mutex
}

// New creates an agent that connects to schedulerURL.
func New(id, schedulerURL, workspaceDir, cacheDir, logDir, vaultAddr, vaultToken, apiToken string, maxGB, maxPercent float64, schedule string, concurrency int) *Agent {
	var vault *secrets.Client
	if vaultAddr != "" && vaultToken != "" {
		vault = secrets.NewClient(vaultAddr, vaultToken)
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return &Agent{
		id:               id,
		schedulerURL:     schedulerURL,
		workspaceDir:     workspaceDir,
		cacheDir:         cacheDir,
		logDir:           logDir,
		vault:            vault,
		apiToken:         apiToken,
		client:           &http.Client{Timeout: 10 * time.Second},
		maxDockerGB:      maxGB,
		maxDockerPercent: maxPercent,
		pruneSchedule:    schedule,
		maxConcurrency:   concurrency,
		semaphore:        make(chan struct{}, concurrency),
		out:              make(chan *pb.AgentMessage, 64),
	}
}

// Run starts the agent's gRPC session and handles jobs. Blocks until ctx is canceled.
func (a *Agent) Run(ctx context.Context) error {
	// Determine gRPC address from schedulerURL or environment variable
	grpcAddrRaw := os.Getenv("FORGE_GRPC_ADDR")
	isSecure := strings.HasPrefix(a.schedulerURL, "https://")
	if strings.HasPrefix(grpcAddrRaw, "https://") {
		isSecure = true
	} else if strings.HasPrefix(grpcAddrRaw, "http://") {
		isSecure = false
	}

	grpcAddr := grpcAddrRaw
	if grpcAddr != "" {
		grpcAddr = strings.TrimPrefix(grpcAddr, "http://")
		grpcAddr = strings.TrimPrefix(grpcAddr, "https://")
		grpcAddr = strings.TrimSuffix(grpcAddr, "/")

		// If no port is specified, add default based on security
		if !strings.Contains(grpcAddr, ":") {
			if isSecure {
				grpcAddr += ":443"
			} else {
				grpcAddr += ":50051"
			}
		}
	} else {
		grpcAddr = "localhost:50051"
		if u, err := url.Parse(a.schedulerURL); err == nil {
			host := u.Hostname()
			if host != "" {
				if isSecure {
					port := u.Port()
					if port == "" {
						port = "443"
					}
					grpcAddr = host + ":" + port
				} else {
					grpcAddr = host + ":50051"
				}
			}
		}
	}

	var opts []grpc.DialOption
	if isSecure {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	opts = append(opts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                10 * time.Second,
		Timeout:             5 * time.Second,
		PermitWithoutStream: true,
	}))

	fmt.Printf("=== FORGE DEBUG V3 === [agent %s] dialing gRPC: %s (secure: %v)\n", a.id[:8], grpcAddr, isSecure)
	conn, err := grpc.NewClient(grpcAddr, opts...)
	if err != nil {
		return fmt.Errorf("failed to dial gRPC: %w", err)
	}
	defer conn.Close()

	a.grpcClient = pb.NewAgentServiceClient(conn)

	// Open bidirectional stream
	fmt.Printf("=== FORGE DEBUG V3 === [agent %s] opening gRPC session...\n", a.id[:8])
	stream, err := a.grpcClient.Session(ctx)
	if err != nil {
		return fmt.Errorf("failed to open gRPC session: %w", err)
	}

	// Register agent
	fmt.Printf("=== FORGE DEBUG V3 === [agent %s] registering agent...\n", a.id[:8])
	err = stream.Send(&pb.AgentMessage{
		AgentId: a.id,
		Payload: &pb.AgentMessage_Register{
			Register: &pb.RegisterRequest{
				Concurrency: int32(a.maxConcurrency),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to register: %w", err)
	}
	fmt.Printf("=== FORGE DEBUG V3 === [agent %s] registration sent\n", a.id[:8])

	// Clean up any dangling containers from previous runs.
	executor.Cleanup()
	defer executor.Cleanup()

	go a.debugLoop(ctx)
	go a.pruneLoop(ctx)
	go a.statusLoop(ctx)

	// Goroutine to send outgoing messages (heartbeats, completions, logs)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-a.out:
				msg.AgentId = a.id
				if err := stream.Send(msg); err != nil {
					fmt.Printf("[agent %s] gRPC send error: %v\n", a.id[:8], err)
				}
			}
		}
	}()

	fmt.Printf("=== FORGE DEBUG V3 === [agent %s] concurrency limit: %d\n", a.id[:8], a.maxConcurrency)
	fmt.Printf("=== FORGE DEBUG V3 === [agent %s] connected and waiting for jobs\n", a.id[:8])

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Printf("=== FORGE DEBUG V3 === [agent %s] still waiting for jobs (stream active)...\n", a.id[:8])
			}
		}
	}()

	// Receive loop: receive jobs from scheduler
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			fmt.Printf("=== FORGE DEBUG V3 === [agent %s] gRPC stream closed by server (EOF)\n", a.id[:8])
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			fmt.Printf("=== FORGE DEBUG V3 === [agent %s] gRPC receive error: %v\n", a.id[:8], err)
			return fmt.Errorf("gRPC receive error: %w", err)
		}

		if ack := msg.GetHeartbeatAck(); ack != nil {
			if ack.Stop {
				if cancel, ok := a.activeJobs.Load(ack.JobId); ok {
					fmt.Printf("[agent %s] stopping job %s as requested by scheduler\n", a.id[:8], ack.JobId[:8])
					cancel.(context.CancelFunc)()
				}
			}
		}

		if pbSpec := msg.GetJob(); pbSpec != nil {
			// Convert pb.JobSpec back to api.JobSpec
			spec := &api.JobSpec{
				JobID:        pbSpec.JobId,
				RunID:        pbSpec.RunId,
				LeaseID:      pbSpec.LeaseId,
				StepID:       pbSpec.StepId,
				Image:        pbSpec.Image,
				Entrypoint:   pbSpec.Entrypoint,
				Command:      pbSpec.Command,
				WorkDir:      pbSpec.WorkDir,
				Env:          pbSpec.Env,
				Inputs:       pbSpec.Inputs,
				SecretNames:  pbSpec.SecretNames,
				DockerSocket: pbSpec.DockerSocket,
				Timeout:      time.Duration(pbSpec.TimeoutNs),
				Type:         pbSpec.Type,
				OrgID:        pbSpec.OrgId,
				ProjectID:    pbSpec.ProjectId,
				CommitSHA:    pbSpec.CommitSha,
				Condition:    pbSpec.Condition,
				AlwaysRun:    pbSpec.AlwaysRun,
			}

			if pbSpec.PipelineRef != nil {
				spec.PipelineRef = &api.PipelineRef{
					Path: pbSpec.PipelineRef.Path,
					Wait: pbSpec.PipelineRef.Wait,
				}
			}

			for _, u := range pbSpec.ArtifactUploads {
				spec.ArtifactUploads = append(spec.ArtifactUploads, api.ArtifactUploadSpec{
					Path: u.Path,
					Name: u.Name,
				})
			}
			for _, d := range pbSpec.ArtifactDownloads {
				spec.ArtifactDownloads = append(spec.ArtifactDownloads, api.ArtifactDownloadSpec{
					Name: d.Name,
					Dest: d.Dest,
				})
			}

			fmt.Printf("[agent %s] received job %s (step: %s) via gRPC\n",
				a.id[:8], spec.JobID[:8], spec.StepID)

			go func(s *api.JobSpec) {
				a.semaphore <- struct{}{}
				defer func() {
					<-a.semaphore
					a.checkDiskUsageAndCleanup()
				}()

				jobCtx, cancel := context.WithCancel(ctx)
				a.activeJobs.Store(s.JobID, cancel)
				defer a.activeJobs.Delete(s.JobID)
				defer cancel()

				if err := a.execute(jobCtx, s); err != nil {
					fmt.Printf("[agent %s] execute error: %v\n", a.id[:8], err)
				}
			}(spec)
		}
	}
}

func (a *Agent) pruneLoop(ctx context.Context) {
	d := 24 * time.Hour
	if a.pruneSchedule == "@hourly" {
		d = time.Hour
	} else if a.pruneSchedule != "@daily" {
		if val, err := time.ParseDuration(a.pruneSchedule); err == nil {
			d = val
		}
	}

	fmt.Printf("[agent %s] Docker prune scheduled every %s\n", a.id[:8], d)
	ticker := time.NewTicker(d)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Printf("[agent %s] running scheduled docker system prune...\n", a.id[:8])
			exec.Command("docker", "system", "prune", "-f").Run()
			// Also clean up any old workspace directories
			a.cleanupWorkspaces()
		}
	}
}

func (a *Agent) checkDiskUsageAndCleanup() {
	a.cleanupMu.Lock()
	defer a.cleanupMu.Unlock()

	// 1. Check Docker disk usage (GB)
	usageGB, err := a.getDockerUsageGB()
	if err != nil {
		fmt.Printf("[agent %s] error checking docker usage: %v\n", a.id[:8], err)
		return
	}

	// 2. Check disk percentage (if configured)
	percent := 0.0
	if a.maxDockerPercent > 0 {
		percent = getDiskUsagePercent(a.workspaceDir)
	}

	// 3. Check if we exceed threshold
	if usageGB > a.maxDockerGB || (a.maxDockerPercent > 0 && percent > a.maxDockerPercent) {
		reason := fmt.Sprintf("Docker usage %.2f GB > %.2f GB", usageGB, a.maxDockerGB)
		if a.maxDockerPercent > 0 && percent > a.maxDockerPercent {
			reason = fmt.Sprintf("Disk usage %.1f%% > %.1f%%", percent, a.maxDockerPercent)
		}

		fmt.Printf("[agent %s] %s, evicting LRU images...\n", a.id[:8], reason)
		// If we are over percent, we might want to free more. But for now let's just free some.
		toFree := usageGB - (a.maxDockerGB * 0.9)
		if toFree < 5 {
			toFree = 5 // free at least 5GB if we are over threshold
		}
		a.evictLRUImages(toFree)
	}
}

func getDiskUsagePercent(path string) float64 {
	if runtime.GOOS == "windows" {
		// Use PowerShell to get disk usage percentage for the drive containing path
		absPath, _ := filepath.Abs(path)
		drive := filepath.VolumeName(absPath)
		if drive == "" {
			drive = "C:"
		}
		cmd := fmt.Sprintf("Get-Volume -DriveLetter %s | ForEach-Object { 100 * (1 - $_.SizeRemaining / $_.Size) }", strings.TrimSuffix(drive, ":"))
		out, err := exec.Command("powershell", "-Command", cmd).Output()
		if err == nil {
			val, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
			return val
		}
	} else {
		// Use df on Linux/Unix
		out, err := exec.Command("df", "--output=pcent", path).Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) >= 2 {
				pStr := strings.TrimSpace(strings.TrimSuffix(lines[1], "%"))
				val, _ := strconv.ParseFloat(pStr, 64)
				return val
			}
		}
	}
	return 0
}

func (a *Agent) getDockerUsageGB() (float64, error) {
	out, err := exec.Command("docker", "system", "df", "--format", "{{.Type}} {{.Size}}").Output()
	if err != nil {
		return 0, err
	}

	var total float64
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// We care about Images and Build Cache
		if fields[0] == "Images" || fields[0] == "Build" { // fields[0] might be "Build Cache" -> "Build"
			sizeStr := fields[len(fields)-1]
			gb := parseDockerSize(sizeStr)
			total += gb
		}
	}
	return total, nil
}

func parseDockerSize(s string) float64 {
	s = strings.ToUpper(s)
	multiplier := 1.0
	if strings.HasSuffix(s, "GB") || strings.HasSuffix(s, "GIB") {
		multiplier = 1.0
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GB"), "GIB")
	} else if strings.HasSuffix(s, "MB") || strings.HasSuffix(s, "MIB") {
		multiplier = 0.001
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MB"), "MIB")
	} else if strings.HasSuffix(s, "KB") || strings.HasSuffix(s, "KIB") {
		multiplier = 0.000001
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KB"), "KIB")
	} else if strings.HasSuffix(s, "B") {
		multiplier = 0.000000001
		s = strings.TrimSuffix(s, "B")
	}

	val, _ := strconv.ParseFloat(s, 64)
	return val * multiplier
}

func (a *Agent) evictLRUImages(targetGB float64) {
	// List all images with ID, CreatedAt, and Size
	out, err := exec.Command("docker", "images", "--format", "{{.ID}}|{{.CreatedAt}}|{{.Size}}").Output()
	if err != nil {
		return
	}

	type imgInfo struct {
		id      string
		created time.Time
		gb      float64
	}
	var images []imgInfo
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		// Docker CreatedAt format can vary, but usually it's "2023-01-01 12:00:00 +0000 UTC"
		// or "About an hour ago" if not using --format.
		// With --format {{.CreatedAt}}, it should be RFC3339-like or similar.
		// Actually let's use a simpler way to sort: docker images --sort=created is not available.
		// But we can parse it.
		created, _ := time.Parse("2006-01-02 15:04:05 -0700 MST", parts[1])
		images = append(images, imgInfo{
			id:      parts[0],
			created: created,
			gb:      parseDockerSize(parts[2]),
		})
	}

	// Sort by creation time (ascending = oldest first)
	slices.SortFunc(images, func(a, b imgInfo) int {
		return a.created.Compare(b.created)
	})

	evictedGB := 0.0
	for _, img := range images {
		if evictedGB >= targetGB {
			break
		}
		// Try to remove. It will fail if in use.
		err := exec.Command("docker", "rmi", img.id).Run()
		if err == nil {
			evictedGB += img.gb
			fmt.Printf("[agent %s] evicted image %s (%.2f GB)\n", a.id[:8], img.id[:12], img.gb)
		}
	}
}

func (a *Agent) cleanupWorkspaces() {
	files, err := os.ReadDir(a.workspaceDir)
	if err != nil {
		return
	}

	now := time.Now()
	for _, f := range files {
		if !f.IsDir() || (!strings.HasPrefix(f.Name(), "forge-job-") && !strings.HasPrefix(f.Name(), "forge-debug-")) {
			continue
		}

		info, err := f.Info()
		if err != nil {
			continue
		}

		// Remove workspaces older than 24 hours
		if now.Sub(info.ModTime()) > 24*time.Hour {
			fmt.Printf("[agent %s] cleaning up old workspace: %s\n", a.id[:8], f.Name())
			os.RemoveAll(filepath.Join(a.workspaceDir, f.Name()))
		}
	}
}

// execute runs a single leased job:
//  1. Starts a heartbeat goroutine
//  2. Builds a pipeline.Step from the spec
//  3. Runs it via the executor
//  4. Reports the result to the scheduler
//  5. Stops the heartbeat goroutine
func (a *Agent) execute(ctx context.Context, spec *api.JobSpec) error {
	ctx, span := tracing.Tracer().Start(ctx, "agent.execute")
	defer span.End()

	/*
		Start heartbeat goroutine

		`done` is a channel we close to signal the heartbeat goroutine to stop.
		As a C# dev, this is the equivalent to `CancellationTokenSource.Cancel()`.
	*/
	done := make(chan struct{})
	defer close(done)
	go a.heartbeatLoop(spec.JobID, spec.LeaseID, done)

	/*
		Runtime Condition Evaluation

		Scheduler already handles success/failure/always conditions via unlockDownstream.
		Here we're evaluating environment variable conditions such as `$BRANCH == "main"` that
		can only be resolved at agent runtime because the scheduler doesn't have access to the step's
		environment
	*/
	if cond := spec.Condition; cond != "" &&
		!isSchedulerCondition(cond) &&
		!evalRuntimeCondition(cond, spec.Env) {
		return a.reportSkipped(spec, cond)
	}

	/*
		Create isolated workspace

		Each job gets its own unique directory to prevent collisions
		during parallel runs on the same agent.
	*/
	jobBaseDir := filepath.Join(a.workspaceDir, "forge-job-"+spec.JobID)
	jobWorkspace := filepath.Join(jobBaseDir, "workspace")
	if err := os.MkdirAll(jobWorkspace, 0755); err != nil {
		err = fmt.Errorf("creating job workspace: %w", err)
		a.reportComplete(spec, 1, 0, []api.LogEvent{{
			Timestamp: time.Now(),
			Level:     "ERROR",
			Message:   err.Error(),
		}}, "", false)
		return err
	}
	defer os.RemoveAll(jobBaseDir)

	/*
		If the job belongs to a repository (ProjectID + CommitSHA are set),
		perform an automatic checkout if the workspace is empty.
		This ensures that injected steps (like security scans) and user steps
		always have the source code available without explicit checkout steps
		needing to share state via artifacts.
	*/
	if spec.ProjectID != "" && spec.CommitSHA != "" {
		files, _ := os.ReadDir(jobWorkspace)
		if len(files) == 0 {
			fmt.Printf("[agent %s] workspace empty, performing checkout for %s @ %s\n",
				a.id[:8], spec.ProjectID, spec.CommitSHA)
			if err := a.performCheckout(ctx, jobWorkspace, spec.ProjectID, spec.CommitSHA); err != nil {
				err = fmt.Errorf("automatic checkout failed: %w", err)
				a.reportComplete(spec, 1, 0, []api.LogEvent{{
					Timestamp: time.Now(),
					Level:     "ERROR",
					Message:   err.Error(),
				}}, "", false)
				return err
			}
		}
	}

	if spec.Type == "pipeline" {
		return a.executePipelineStep(ctx, spec, jobWorkspace)
	}

	jobLogDir := filepath.Join(a.logDir, spec.JobID)
	defer os.RemoveAll(jobLogDir)
	exec, err := executor.New(jobWorkspace, jobLogDir, a.cacheDir)
	if err != nil {
		err = fmt.Errorf("creating executor: %w", err)
		a.reportComplete(spec, 1, 0, []api.LogEvent{{
			Timestamp: time.Now(),
			Level:     "ERROR",
			Message:   err.Error(),
		}}, "", false)
		return err
	}
	exec.UseCopy = true

	// Convert API Spec -> pipeline.Step
	step := &pipeline.Step{
		ID:           spec.StepID,
		Name:         spec.StepID,
		Image:        spec.Image,
		Entrypoint:   spec.Entrypoint,
		Command:      spec.Command,
		WorkDir:      spec.WorkDir,
		Env:          spec.Env,
		Inputs:       spec.Inputs,
		Timeout:      spec.Timeout,
		Secrets:      spec.SecretNames,
		DockerSocket: spec.DockerSocket,
		Type:         spec.Type,
	}

	/*
		Default WorkDir to /workspace if not set
		Policy-injected steps from transformers may often omit WorkDir since then
		transformer JSON doesn't know about Forge's defaults. Without this,
		Docker gets --workdir "" and fails before writing any log output.

		Thus, the default is /workspace to avoid this failure.
	*/
	if step.WorkDir == "" {
		step.WorkDir = "/workspace"
	}

	/*
		Inject FORGE_API_TOKEN, and FORGE_SCHEDULER_URL so steps
		can communicate with the scheduler as needed (e.g. for the injected checkout step).
	*/
	if step.Env == nil {
		step.Env = make(map[string]string)
	}
	step.Env["FORGE_API_TOKEN"] = a.apiToken
	step.Env["FORGE_SCHEDULER_URL"] = a.schedulerURL

	/*
		Fetch secrets from Vault

		Secrets are injected just-in-time here - never stored within the scheduler or passed through the job queue. The
		vault exists only in this function's stack frame and in the container's environment.
	*/
	if len(spec.SecretNames) > 0 {
		if a.vault == nil {
			err := fmt.Errorf("step %q requires secrets %v but FORGE_VAULT_ADDR / FORGE_VAULT_TOKEN are not set",
				spec.StepID, spec.SecretNames)
			a.reportComplete(spec, 1, 0, []api.LogEvent{{
				Timestamp: time.Now(),
				Level:     "ERROR",
				Message:   err.Error(),
			}}, "", false)
			return err
		}
		if step.Env == nil {
			step.Env = make(map[string]string)
		}
		for _, name := range spec.SecretNames {
			val, err := a.vault.GetScoped(name, spec.OrgID, spec.ProjectID)
			if err != nil {
				err = fmt.Errorf("fetching secret %q: %w", name, err)
				a.reportComplete(spec, 1, 0, []api.LogEvent{{
					Timestamp: time.Now(),
					Level:     "ERROR",
					Message:   err.Error(),
				}}, "", false)
				return err
			}

			// We inject the secret value using the same name as the secret
			step.Env[name] = val

			/*
				Register the value for log redaction so it never appears in the log output
				even if the step accidentally echos it.
			*/
			step.RedactValues = append(step.RedactValues, val)
		}
		scopeDesc := "global"
		if spec.ProjectID != "" {
			scopeDesc = fmt.Sprintf("project %s", spec.ProjectID)
		} else if spec.OrgID != "" {
			scopeDesc = fmt.Sprintf("org %s", spec.OrgID)
		}
		fmt.Printf("[agent %s] fetched %d secret(s) for step %s (scope: %s)\n",
			a.id[:8], len(spec.SecretNames), spec.StepID, scopeDesc)
	}

	// Check CAS before running
	if a.cacheDir != "" && len(step.Inputs) > 0 {
		cas, err := cache.New(a.cacheDir)
		if err == nil {
			hash, err := cache.ComputeTaskHash(step, jobWorkspace)
			if err == nil {
				step.CacheKey = hash
				if entry, hit := cas.Lookup(hash); hit {
					fmt.Printf("[agent %s] cache hit for step %s\n",
						a.id[:8], step.ID)
					return a.reportComplete(spec, entry.ExitCode, 0, cacheHitLog(hash), "", false)
				}
			}
		}
	}

	// Download artifacts declared by this step
	if len(spec.ArtifactDownloads) > 0 {
		fmt.Printf("=== FORGE DEBUG V3 === [agent %s] downloading %d artifact(s)\n", a.id[:8], len(spec.ArtifactDownloads))
		if err := a.downloadArtifacts(spec, jobWorkspace); err != nil {
			fmt.Printf("[agent %s] artifact download failed: %v\n", a.id[:8], err)
			return a.reportComplete(spec, 1, 0, []api.LogEvent{{
				Timestamp: time.Now(),
				Level:     "ERROR",
				Message:   fmt.Sprintf("artifact download failed: %v", err),
			}}, "", false)
		}
		// List workspace after download
		if files, err := os.ReadDir(jobWorkspace); err == nil {
			fmt.Printf("=== FORGE DEBUG V3 === [agent %s] workspace after download:\n", a.id[:8])
			for _, f := range files {
				fmt.Printf("  - %s (%v bytes)\n", f.Name(), func() int64 {
					i, _ := f.Info()
					if i != nil {
						return i.Size()
					}
					return 0
				}())
			}
		}
	}

	// Run the step
	start := time.Now()
	stepCtx, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()

	/*
		Set up real-time log streaming
		A buffered channel decouples the executor's log writes from the
		HTTP POST to the scheduler - the scanner never blocks.
	*/
	logCh := make(chan api.LogEvent, 256)

	exec.StreamCallback = func(stepID string, ts time.Time, level, message string) {
		select {
		case logCh <- api.LogEvent{Timestamp: ts, Level: level, Message: message}:
		default:
		}
	}

	// Streaming goroutine: batches events and POSTs them every 500ms.
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		a.streamJobLogs(spec.JobID, spec.LeaseID, logCh)
	}()

	result, err := exec.RunStep(stepCtx, step)
	elapsed := time.Since(start)

	timedOut := false
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		timedOut = true
		fmt.Printf("[agent %s] step %s timed out after %v\n", a.id[:8], spec.StepID, step.Timeout)
	}

	// Signal the streaming goroutine to flush and exit
	close(logCh)
	<-streamDone

	exitCode := 0
	if err != nil {
		exitCode = 1
	} else if result != nil {
		exitCode = result.ExitCode
	}

	// Read log events to forward to the scheduler.
	var logEvents []api.LogEvent
	if timedOut {
		logEvents = []api.LogEvent{{
			Timestamp: time.Now(),
			Level:     "ERROR",
			Message:   fmt.Sprintf("◯ step timed out after %v", step.Timeout),
		}}
	} else if err != nil {
		/*
				Hard error from the executor - e.g. Docker failed to start the container due to workdir being set to "".
			    The result is nil, so there's no log file. We'll sythesize a log event so the user sees what went wrong
			    in the UI rather than "no logs stored for this job"
		*/
		logEvents = []api.LogEvent{{
			Timestamp: time.Now(),
			Level:     "ERROR",
			Message:   fmt.Sprintf("executor error: %v", err),
		}}
	} else if result != nil && result.CacheHit {
		logEvents = cacheHitLog(result.Step.CacheKey)
	} else if result != nil && result.LogFile != "" {
		logEvents = readLogFile(result.LogFile)
	}
	if logEvents == nil {
		logEvents = []api.LogEvent{}
	}

	// For generator steps, parse the emitted step definitions from stdout.
	var emittedStepsJSON string
	if result != nil && len(result.GeneratedStepsJSON) > 0 && exitCode == 0 {
		emittedStepsJSON = string(result.GeneratedStepsJSON)
	}

	// Upload artifacts declared by this step (only on success)
	if exitCode == 0 && len(spec.ArtifactUploads) > 0 {
		a.uploadArtifacts(spec, jobWorkspace)
	}

	return a.reportComplete(spec, exitCode, elapsed.Milliseconds(), logEvents, emittedStepsJSON, timedOut)
}

func (a *Agent) statusLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.out <- &pb.AgentMessage{
				Payload: &pb.AgentMessage_Heartbeat{
					Heartbeat: &pb.HeartbeatRequest{
						Status: a.collectStatus(),
					},
				},
			}
		}
	}
}

func (a *Agent) collectStatus() *pb.AgentStatus {
	status := &pb.AgentStatus{
		ActiveJobsCount: int32(len(a.semaphore)),
		Version:         "v0.1.0",
	}

	// Docker images count
	cmd := exec.Command("docker", "images", "-q")
	if out, err := cmd.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		count := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				count++
			}
		}
		status.DockerImagesCount = int32(count)
	}
	return status
}

// heartbeatLoop sends heartbeats every heartbeatInterval until done is closed.
// Runs as a goroutine alongside the executing job.
func (a *Agent) heartbeatLoop(jobID, leaseID string, done <-chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := a.heartbeat(jobID, leaseID); err != nil {
				fmt.Printf("[agent %s] heartbeat failed: %v\n", a.id[:8], err)
				return
			}
		case <-done:
			return // job finished - stop heartbeat
		}
	}
}

// lease asks the scheduler for the next available job.
func (a *Agent) lease(ctx context.Context) (*api.JobSpec, bool, error) {
	body, _ := json.Marshal(api.LeaseRequest{AgentID: a.id})

	req, _ := http.NewRequestWithContext(ctx, "POST",
		a.schedulerURL+"/api/v1/jobs/lease",
		bytes.NewReader(body),
	)
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	// 204 - queue is empty
	if resp.StatusCode == http.StatusNoContent {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("lease: unexpected status %d", resp.StatusCode)
	}

	var leaseResp api.LeaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&leaseResp); err != nil {
		return nil, false, fmt.Errorf("decoding lease response: %w", err)
	}
	return leaseResp.Job, true, nil
}

// heartbeat notifies the scheduler that this agent is still alive via gRPC.
func (a *Agent) heartbeat(jobID, leaseID string) error {
	a.out <- &pb.AgentMessage{
		Payload: &pb.AgentMessage_Heartbeat{
			Heartbeat: &pb.HeartbeatRequest{
				JobId:   jobID,
				LeaseId: leaseID,
				Status:  a.collectStatus(),
			},
		},
	}
	return nil
}

// reportComplete sends the job result to the scheduler via gRPC.
func (a *Agent) reportComplete(spec *api.JobSpec, exitCode int, durationMs int64, logs []api.LogEvent, emittedStepsJSON string, timedOut bool) error {
	pbLogs := make([]*pb.LogEvent, len(logs))
	for i, l := range logs {
		pbLogs[i] = &pb.LogEvent{
			Ts:      timestamppb.New(l.Timestamp),
			Level:   l.Level,
			Message: l.Message,
		}
	}
	a.out <- &pb.AgentMessage{
		Payload: &pb.AgentMessage_Complete{
			Complete: &pb.CompleteRequest{
				JobId:            spec.JobID,
				LeaseId:          spec.LeaseID,
				ExitCode:         int32(exitCode),
				DurationMs:       durationMs,
				Logs:             pbLogs,
				EmittedStepsJson: emittedStepsJSON,
				Skipped:          false,
				TimedOut:         timedOut,
			},
		},
	}
	return nil
}

// reportSkipped marks a job as skipped via gRPC.
func (a *Agent) reportSkipped(spec *api.JobSpec, condition string) error {
	fmt.Printf("[agent %s] step %s skipped — condition %q evaluated to false\n",
		a.id[:8], spec.StepID, condition)
	pbLogs := []*pb.LogEvent{{
		Ts:      timestamppb.Now(),
		Level:   "INFO",
		Message: fmt.Sprintf("◯ step skipped: condition %q is false", condition),
	}}
	a.out <- &pb.AgentMessage{
		Payload: &pb.AgentMessage_Complete{
			Complete: &pb.CompleteRequest{
				JobId:    spec.JobID,
				LeaseId:  spec.LeaseID,
				ExitCode: 0,
				Logs:     pbLogs,
				Skipped:  true,
			},
		},
	}
	return nil
}

// isSchedulerCondition returns true for conditions the scheduler evaluates
// itself (success(), failure(), always()).  These must not be re-evaluated
// by the agent since the scheduler has already acted on them.
func isSchedulerCondition(cond string) bool {
	c := strings.TrimSpace(strings.ToLower(cond))
	return c == "" || c == "success()" || c == "failure()" || c == "always()" || c == "tag()"
}

// evalRuntimeCondition evaluates an environment-variable condition expression.
// Supported syntax:
//
//	$VAR == 'value'   — string equality
//	$VAR != 'value'   — string inequality
//	$VAR              — truthy: non-empty
//	!$VAR             — falsy: empty
func evalRuntimeCondition(expr string, env map[string]string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	resolved := resolveEnvRefs(expr, env)

	if idx := strings.Index(resolved, "=="); idx >= 0 {
		left := strings.TrimSpace(resolved[:idx])
		right := strings.Trim(strings.TrimSpace(resolved[idx+2:]), "'\"")
		return left == right
	}
	if idx := strings.Index(resolved, "!="); idx >= 0 {
		left := strings.TrimSpace(resolved[:idx])
		right := strings.Trim(strings.TrimSpace(resolved[idx+2:]), "'\"")
		return left != right
	}
	if strings.HasPrefix(resolved, "!") {
		return strings.TrimSpace(resolved[1:]) == ""
	}
	return strings.TrimSpace(resolved) != ""
}

// resolveEnvRefs replaces $VAR and ${VAR} references with values from env.
// Variables not present in env resolve to empty string — an unset variable
// is treated as falsy, consistent with shell behaviour.
func resolveEnvRefs(expr string, env map[string]string) string {
	result := expr
	// First pass: replace known variables.
	for k, v := range env {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
		result = strings.ReplaceAll(result, "$"+k, v)
	}
	// Second pass: collapse any remaining $IDENTIFIER or ${IDENTIFIER}
	// references to empty string (variable not set → treat as empty).
	for strings.Contains(result, "$") {
		before := result
		// ${VAR} form
		if start := strings.Index(result, "${"); start >= 0 {
			if end := strings.Index(result[start:], "}"); end >= 0 {
				result = result[:start] + result[start+end+1:]
				continue
			}
		}
		// $VAR form — consume $IDENTIFIER (letters, digits, underscores)
		if idx := strings.Index(result, "$"); idx >= 0 {
			end := idx + 1
			for end < len(result) && (result[end] == '_' ||
				(result[end] >= 'A' && result[end] <= 'Z') ||
				(result[end] >= 'a' && result[end] <= 'z') ||
				(result[end] >= '0' && result[end] <= '9')) {
				end++
			}
			result = result[:idx] + result[end:]
			continue
		}
		if result == before {
			break // nothing more to replace
		}
	}
	return result
}

// sleep waits for d duration but returns early if ctx is canceled.
// This is preferred due to time.Sleep() not allowing interruption by context cancellation.
func (a *Agent) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// cacheHitLog returns a single synthetic log event for a cache hit.
// This is what the UI panel shows when a step is skipped because its inputs haven't changed.
func cacheHitLog(taskHash string) []api.LogEvent {
	short := taskHash
	if len(short) > 12 {
		short = short[:12]
	}
	return []api.LogEvent{{
		Timestamp: time.Now(),
		Level:     "INFO",
		Message:   fmt.Sprintf("◎ cache hit — step skipped (hash: %s...)", short),
	}}
}

// readLogFile reads a JSONL log file written by the executor and converts
// each line into an api.LogEvent for forwarding to the scheduler.
//
// JSONL (JSON Lines) means one complete JSON object per line. This is far easier to parse.
//
// Errors on individual lines are silently skipped rather than aborting
// a partial log is better than no log if the step crashed mid-write
func readLogFile(path string) []api.LogEvent {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	// A struct that matches only the fields we care about from the
	// logger's Event type. We don't need to import the log package —
	// just declare what we want to pull out.
	type rawEvent struct {
		Timestamp string `json:"ts"`
		Level     string `json:"level"`
		Message   string `json:"message"`
	}

	var events []api.LogEvent
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var raw rawEvent
		if err := json.Unmarshal(line, &raw); err != nil {
			continue // Skip malformed lines (could be due to crash)
		}
		ts, err := time.Parse(time.RFC3339Nano, raw.Timestamp)
		if err != nil {
			ts = time.Now()
		}
		events = append(events, api.LogEvent{
			Timestamp: ts,
			Level:     raw.Level,
			Message:   raw.Message,
		})
	}
	return events
}

// debugLoop polls for debug sessions and handles them concurrently with jobs.
func (a *Agent) debugLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		spec, ok, err := a.leaseDebug(ctx)
		if err != nil {
			fmt.Printf("=== FORGE DEBUG V3 === [agent %s] debug lease error: %v\n", a.id[:8], err)
			a.sleep(ctx, 5*time.Second)
			continue
		}
		if !ok {
			a.sleep(ctx, 2*time.Second)
			continue
		}
		fmt.Printf("[agent %s] starting debug container for session %s\n",
			a.id[:8], spec.SessionID[:8])
		go a.handleDebugSession(ctx, spec)
	}
}

// handleDebugSession starts a debug container and relays commands until closed.
func (a *Agent) handleDebugSession(ctx context.Context, spec *api.DebugJobSpec) {
	// Always use an isolated workspace for each debug session to avoid Bug 2.
	baseDir := filepath.Join(a.workspaceDir, "forge-debug-"+spec.SessionID)
	workspaceDir := filepath.Join(baseDir, "workspace")
	os.MkdirAll(workspaceDir, 0755)
	defer os.RemoveAll(baseDir)

	// Bug 1: If the workspace is empty and we have repo info, perform a checkout.
	if spec.ProjectID != "" && spec.CommitSHA != "" {
		files, _ := os.ReadDir(workspaceDir)
		if len(files) == 0 {
			fmt.Printf("[agent %s] workspace empty, performing checkout for %s @ %s\n",
				a.id[:8], spec.ProjectID[:8], spec.CommitSHA[:8])
			if err := a.performCheckout(ctx, workspaceDir, spec.ProjectID, spec.CommitSHA); err != nil {
				fmt.Printf("[agent %s] debug checkout failed: %v\n", a.id[:8], err)
				return
			}
		}
	}

	workDir := spec.WorkDir
	if workDir == "" {
		workDir = "/workspace"
	}

	args := []string{
		"create",
		"--label", "forge.managed=true",
		"--label", "forge.debug=true",
		"--workdir", workDir,
	}
	if spec.DockerSocket {
		hostSocket := "/var/run/docker.sock"
		if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
			hostSocket = strings.TrimPrefix(h, "unix://")
		} else if runtime.GOOS == "windows" {
			hostSocket = `\\.\pipe\docker_engine`
		}
		args = append(args, "--volume", hostSocket+":/var/run/docker.sock")
		args = append(args, "-e", "DOCKER_HOST=unix:///var/run/docker.sock")
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, spec.Image, "tail", "-f", "/dev/null")

	fmt.Printf("[agent %s] creating debug container for session %s with workdir %s\n",
		a.id[:8], spec.SessionID[:8], workDir)
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		fmt.Printf("[agent %s] debug container failed to create: %v\n", a.id[:8], err)
		return
	}
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		return
	}

	// Copy workspace into debug container
	// We copy the host's "workspace" directory into the container's root.
	src := filepath.Clean(workspaceDir)
	cpCmd := exec.CommandContext(ctx, "docker", "cp", src, containerID+":/")
	if err := cpCmd.Run(); err != nil {
		fmt.Printf("[agent %s] failed to copy workspace into debug container: %v\n", a.id[:8], err)
		exec.Command("docker", "rm", "-f", containerID).Run()
		return
	}

	// Start debug container
	if err := exec.CommandContext(ctx, "docker", "start", containerID).Run(); err != nil {
		fmt.Printf("[agent %s] debug container failed to start: %v\n", a.id[:8], err)
		exec.Command("docker", "rm", "-f", containerID).Run()
		return
	}

	defer func() {
		exec.Command("docker", "stop", containerID).Run()
		exec.Command("docker", "rm", "-f", containerID).Run()
		a.debugConts.Delete(spec.SessionID)
		fmt.Printf("[agent %s] debug container %s stopped\n", a.id[:8], containerID[:12])
	}()

	/*
			Install util-linux on Alpine images - it provides the `script` command which
		    handleTerminalWS uses to allocate a PTY inside the container.
			Runs before we register with the scheduler, so the browser only gets the
		   `ready` signal once `script` is available.
			On non-alpine images `apk` doesn't exist -> exits non-zero -> || true no-ops.
	*/
	fmt.Printf("[agent %s] preparing terminal tools in %s…\n", a.id[:8], containerID[:12])
	exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c",
		"apk add -q --no-cache util-linux >/dev/null 2>&1 || true",
	).Run()

	// Store container ID so the WS terminal handler can look it up be session.
	a.debugConts.Store(spec.SessionID, containerID)

	/*
		Register that we are already. The scheduler will now know this agent is
		handling the session. We no longer need to provide a terminal URL
		dbecause we use the reverse connection model
	*/
	if err := a.registerDebugContainer(spec.SessionID, containerID, ""); err != nil {
		fmt.Printf("[agent %s] failed to register debug container: %v\n", a.id[:8], err)
		return
	}
	fmt.Printf("[agent %s] debug container ready: %s\n",
		a.id[:8], containerID[:12])

	// cancelCmd holds the cancel function for the currently running command.
	// The cancel-poll goroutine calls it when the browser requests a cancel.
	var (
		cancelMu  sync.Mutex
		cancelCmd context.CancelFunc
	)

	/*
	   This is an important goroutine: polls the cancel-check endpoint every 500ms.
	   uses a SEPARATE endpoint from pollDebugCommands so it never accidentally drains
	   the command queue.
	*/
	stopCancel := make(chan struct{})
	defer close(stopCancel)

	go func() {
		for {
			select {
			case <-stopCancel:
				return
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
				shouldCancel, sessionExists := a.checkDebugCancel(spec.SessionID)
				if !sessionExists {
					return
				}
				if shouldCancel {
					cancelMu.Lock()
					if cancelCmd != nil {
						cancelCmd()
					}
					cancelMu.Unlock()
				}
			}
		}
	}()

	// Execute commands sequentially.
	for {
		if ctx.Err() != nil {
			return
		}

		resp, err := a.pollDebugCommands(spec.SessionID)
		if err != nil {
			a.sleep(ctx, time.Second)
			continue
		}
		if resp.Closed {
			return
		}

		for _, cmd := range resp.Commands {
			// Check if this is a reverse terminal request.
			if strings.Contains(cmd.Input, `"type":"terminal_request"`) {
				go a.handleTerminalRequest(ctx, spec.SessionID, containerID, cmd)
				continue
			}

			cmdCtx, cancel := context.WithCancel(ctx)
			cancelMu.Lock()
			cancelCmd = cancel
			cancelMu.Unlock()

			a.execDebugCommand(cmdCtx, spec.SessionID, containerID, cmd)

			cancelMu.Lock()
			cancelCmd = nil
			cancelMu.Unlock()
			cancel()
		}

		if len(resp.Commands) == 0 {
			a.sleep(ctx, 500*time.Millisecond)
		}
	}
}

// execDebugCommand runs a single command in the debug container via docker exec.
// Output is streamed line-by-line so the user sees it in real-time.
// The ctx can be cancelled to kill the command (used by the cancel button).
func (a *Agent) execDebugCommand(ctx context.Context, sessionID, containerID string, cmd api.DebugCommand) {
	execCmd := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", cmd.Input)

	pr, pw, err := os.Pipe()
	if err != nil {
		a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
			CommandID: cmd.CommandID, Output: "error: " + err.Error() + "\n", ExitCode: 1,
		})
		return
	}
	execCmd.Stdout = pw
	execCmd.Stderr = pw

	if err := execCmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
			CommandID: cmd.CommandID, Output: "error starting: " + err.Error() + "\n", ExitCode: 1,
		})
		return
	}

	// Stream output line-by-line while the command runs.
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
				CommandID: cmd.CommandID,
				Output:    scanner.Text() + "\n",
				ExitCode:  -1, // -1 = streaming, final exit code sent below
			})
		}
	}()

	exitCode := 0
	if err := execCmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}
	pw.Close()
	<-scanDone
	pr.Close()

	// Final event: empty output, real exit code - signals completion to the browser.
	a.submitDebugOutput(sessionID, api.SubmitOutputRequest{
		CommandID: cmd.CommandID, Output: "", ExitCode: exitCode,
	})
}

func (a *Agent) leaseDebug(ctx context.Context) (*api.DebugJobSpec, bool, error) {
	body, _ := json.Marshal(api.LeaseRequest{AgentID: a.id})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		a.schedulerURL+"/api/v1/debug/lease", bytes.NewReader(body))
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("debug lease: status %d", resp.StatusCode)
	}
	var spec api.DebugJobSpec
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		return nil, false, err
	}
	return &spec, true, nil
}

func (a *Agent) registerDebugContainer(sessionID, containerID, terminalURL string) error {
	body, _ := json.Marshal(api.RegisterContainerRequest{
		SessionID:     sessionID,
		ContainerID:   containerID,
		AgentID:       a.id,
		TerminalWsURL: terminalURL,
	})
	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/debug/%s/container", a.schedulerURL, sessionID),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (a *Agent) checkDebugCancel(sessionID string) (shouldCancel bool, sessionExists bool) {
	resp, err := a.authGet(
		fmt.Sprintf("%s/api/v1/debug/%s/cancel-check", a.schedulerURL, sessionID))
	if err != nil {
		return false, true
	}
	defer resp.Body.Close()
	var result struct {
		Cancel bool `json:"cancel"`
		Closed bool `json:"closed"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Cancel, !result.Closed
}

func (a *Agent) pollDebugCommands(sessionID string) (*api.PollCommandsResponse, error) {
	resp, err := a.authGet(
		fmt.Sprintf("%s/api/v1/debug/%s/commands", a.schedulerURL, sessionID))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result api.PollCommandsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *Agent) submitDebugOutput(sessionID string, req api.SubmitOutputRequest) {
	body, _ := json.Marshal(req)
	resp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/debug/%s/output", a.schedulerURL, sessionID),
		"application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}

// streamJobLogs reads log events from ch and POSTs them to the scheduler
// in batches - either every 500ms or every 50 events, whichever comes first.
//
// The batching avoids hammering the scheduler with one HTTP request per line
// while still keeping latency low enough that the browser feels real-time.
func (a *Agent) streamJobLogs(jobID, leaseID string, ch <-chan api.LogEvent) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var buf []api.LogEvent

	flush := func() {
		if len(buf) == 0 {
			return
		}
		a.postLogBatch(jobID, leaseID, buf)
		buf = buf[:0]
	}

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				// Channel closed - step finished. Flush remaining events.
				flush()
				return
			}
			buf = append(buf, e)
			if len(buf) >= 50 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// authPost makes an authenticated POST to the scheduler.
func (a *Agent) authPost(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	return a.client.Do(req)
}

// authGet makes an authenticated GET to the scheduler.
func (a *Agent) authGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if a.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	return a.client.Do(req)
}

// rebaseURL replaces the schema+host of a URL with the agent's known scheduler address
// - but ONLY when the URL points at the scheduler itself (detected by comparing paths, not hosts, since FORGE_BASE_URL
// may use "localhost").
//
// This is necessary because FORGE_BASE_URL may be "http://localhost:8080" for browser access, but
// agents run inside Docker where `localhost` resolves to the agent container, not the scheduler.
//
// URLs pointing sat S3/MinIO (pre-signed, different path prefix) are returned unchanged
// so they still hit the object store directly.
func (a *Agent) rebaseURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}

	base, err := url.Parse(a.schedulerURL)
	if err != nil {
		return rawURL
	}

	// Only rebase URLs whose path starts with /api/v1/ - those are
	// scheduler endpoints.
	if strings.HasPrefix(u.Path, "/api/v1/") {
		u.Scheme = base.Scheme
		u.Host = base.Host
		return u.String()
	}

	return rawURL
}

func (a *Agent) postLogBatch(jobID, leaseID string, events []api.LogEvent) {
	pbEvents := make([]*pb.LogEvent, len(events))
	for i, e := range events {
		pbEvents[i] = &pb.LogEvent{
			Ts:      timestamppb.New(e.Timestamp),
			Level:   e.Level,
			Message: e.Message,
		}
	}
	a.out <- &pb.AgentMessage{
		Payload: &pb.AgentMessage_LogBatch{
			LogBatch: &pb.LogBatch{
				JobId:   jobID,
				LeaseId: leaseID,
				Events:  pbEvents,
			},
		},
	}
}

func (a *Agent) handleTerminalRequest(ctx context.Context, sessionID, containerID string, cmd api.DebugCommand) {
	var req struct {
		TerminalID string `json:"terminal_id"`
		Cols       int    `json:"cols"`
		Rows       int    `json:"rows"`
	}
	if err := json.Unmarshal([]byte(cmd.Input), &req); err != nil {
		fmt.Printf("[agent] failed to unmarshal terminal request: %v\n", err)
		return
	}

	u, err := url.Parse(a.schedulerURL)
	if err != nil {
		return
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = fmt.Sprintf("/api/v1/debug/%s/agent-ws", sessionID)
	q := u.Query()
	q.Set("terminalID", req.TerminalID)
	u.RawQuery = q.Encode()

	header := http.Header{}
	if a.apiToken != "" {
		header.Set("Authorization", "Bearer "+a.apiToken)
	}

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		fmt.Printf("[agent] reverse terminal dial failed: %v\n", err)
		return
	}
	defer conn.Close()

	a.pipeTerminalToConn(ctx, sessionID, containerID, conn, req.Cols, req.Rows)
}

func (a *Agent) pipeTerminalToConn(ctx context.Context, sessionID, containerID string, conn *websocket.Conn, cols, rows int) {
	if cols <= 0 {
		cols = 220
	}
	if rows <= 0 {
		rows = 50
	}

	shellCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	shellCmd := fmt.Sprintf(
		`export TERM=xterm-256color COLUMNS=%d LINES=%d; `+
			`if command -v script >/dev/null 2>&1; then exec script -q -c "/bin/sh" /dev/null; `+
			`elif command -v python3 >/dev/null 2>&1; then exec python3 -c 'import pty; pty.spawn("/bin/sh")'; `+
			`elif command -v python >/dev/null 2>&1; then exec python -c 'import pty; pty.spawn("/bin/sh")'; `+
			`else exec sh -i; fi`,
		cols, rows,
	)

	cmd := exec.CommandContext(shellCtx, "docker", "exec",
		"-i",
		"-e", fmt.Sprintf("COLUMNS=%d", cols),
		"-e", fmt.Sprintf("LINES=%d", rows),
		containerID,
		"sh", "-c", shellCmd,
	)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n\x1b[31mFailed to create stdout pipe: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	cmd.Stderr = cmd.Stdout // Merge stderr so Docker errors appear in terminal

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage,
			[]byte("\r\n\x1b[31mFailed to create stdin pipe: "+err.Error()+"\x1b[0m\r\n"))
		return
	}

	if err := cmd.Start(); err != nil {
		fmt.Printf("[agent] failed to start docker exec: %v\n", err)
		conn.WriteMessage(websocket.TextMessage,
			[]byte(fmt.Sprintf("\r\n\x1b[31mFailed to exec into container: %v\x1b[0m\r\n", err)))
		return
	}
	defer cmd.Wait()

	fmt.Printf("[agent %s] terminal reverse-connected — session %s (%dx%d)\n",
		a.id[:8], sessionID[:8], cols, rows)

	// Goroutine: container output -> WebSocket
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				if wErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); wErr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// Main loop: WebSocket input -> container stdin.
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Detect resize control message
		if len(msg) > 0 && msg[0] == '{' {
			var ctrl api.TerminalResizeMsg
			if json.Unmarshal(msg, &ctrl) == nil && ctrl.Type == "resize" {
				go func(c, r int) {
					stdinPipe.Write([]byte(fmt.Sprintf("stty cols %d rows %d\n", c, r)))
				}(ctrl.Cols, ctrl.Rows)
				continue
			}
		}

		if _, err := stdinPipe.Write(msg); err != nil {
			break
		}
	}
}

// downloadArtifacts fetches declared artifacts from the scheduler before the step runs.
func (a *Agent) downloadArtifacts(spec *api.JobSpec, workspaceDir string) error {
	for _, dl := range spec.ArtifactDownloads {
		meta, err := a.getArtifact(spec.RunID, dl.Name)
		if err != nil {
			return fmt.Errorf("artifact %q: %w", dl.Name, err)
		}

		/*
				Scheduler may return a download URL that uses FORGE_BASE_URL (e.g. http://localhost:8080).
			    Inside a Docker container, localhost refers to the agent itself, not the scheduler. Replace the
			    URL's host with the known scheduler address so the download always works from inside Docker.
		*/
		downloadURL := a.rebaseURL(meta.DownloadURL)

		dest := dl.Dest
		if dest == "" {
			dest = dl.Name
		}
		if !strings.HasPrefix(dest, "/") {
			dest = filepath.Join(workspaceDir, dest)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return fmt.Errorf("creating download dir for %q: %w", dl.Name, err)
		}
		if err := a.downloadFile(downloadURL, dest); err != nil {
			return fmt.Errorf("downloading artifact %q: %w", dl.Name, err)
		}
		fmt.Printf("=== FORGE DEBUG V3 === [agent %s] downloaded artifact %q → %s\n", a.id[:8], dl.Name, dest)
	}
	return nil
}

// uploadArtifacts stores declared artifacts after a step succeeds.
func (a *Agent) uploadArtifacts(spec *api.JobSpec, workspaceDir string) {
	for _, ul := range spec.ArtifactUploads {
		pattern := ul.Path
		if !strings.HasPrefix(pattern, "/") {
			pattern = filepath.Join(workspaceDir, pattern)
		}

		matches, err := filepath.Glob(pattern)

		if err != nil || len(matches) == 0 {
			fmt.Printf("[agent %s] artifact pattern %q matched no files in %s\n", a.id[:8], ul.Path, workspaceDir)
			continue
		}

		for _, filePath := range matches {
			name := ul.Name
			if name == "" {
				name = filepath.Base(filePath)
			}
			if err := a.uploadArtifact(spec.RunID, spec.JobID, name, filePath); err != nil {
				fmt.Printf("[agent %s] artifact upload %q failed: %v\n", a.id[:8], name, err)
			} else {
				fmt.Printf("[agent %s] uploaded artifact %q → %s\n", a.id[:8], name, filePath)
			}
		}
	}
}

func (a *Agent) getArtifact(runId, name string) (*api.ArtifactMeta, error) {
	url := fmt.Sprintf("%s/api/v1/artifacts?run_id=%s&name=%s", a.schedulerURL, runId, name)
	resp, err := a.authGet(url)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	var meta api.ArtifactMeta
	json.NewDecoder(resp.Body).Decode(&meta)
	return &meta, nil
}

func (a *Agent) uploadArtifact(runId, jobId, name, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}

	defer f.Close()

	info, _ := f.Stat()
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	contentType := mime.TypeByExtension(filepath.Ext(filePath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Step 1: Get presigned URL
	body, _ := json.Marshal(api.PresignUploadRequest{
		RunID:       runId,
		JobID:       jobId,
		Name:        name,
		Filename:    filepath.Base(filePath),
		ContentType: contentType,
	})

	presignResp, err := a.authPost(
		a.schedulerURL+"/api/v1/artifacts/presign",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("presign: %w", err)
	}

	defer presignResp.Body.Close()
	var presign api.PresignUploadResponse
	json.NewDecoder(presignResp.Body).Decode(&presign)

	if presign.ArtifactID == "" {
		return fmt.Errorf("invalid presign response")
	}

	/*
			Step 2: PUT the file to the upload URL.
		    For local backend: URL may use FORGE_BASE_URL (e.g. localhost:8080) which
		    isn't reachable from inside Docker -rebase to the agent's scheduler address.

		   For S3: URL is a real pre-signed S3 URL - rebaseURL returns it unchanged
		   because its host differs from the scheduler's host.
	*/
	uploadURL := a.rebaseURL(presign.UploadURL)
	req, err := http.NewRequest("PUT", uploadURL, f)
	if err != nil {
		return err
	}

	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)

	/*
			Only add Bearer auth for scheduler endpoints (local backend).
		    S3/Minio pre-signed URLs embed AWS SigV4 credentials in the URL itself
		    and most NOT receive additional Authorization headers.
	*/
	if u, err2 := url.Parse(uploadURL); err2 == nil && strings.HasPrefix(u.Path, "/api/v1/") {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	putResp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT upload: %w", err)
	}
	io.Copy(io.Discard, putResp.Body)
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent && putResp.StatusCode != http.StatusOK {
		return fmt.Errorf("artifact upload returned HTTP %d", putResp.StatusCode)
	}

	// Step 3: confirm (only needed for local backend; S3 confirms automatically).
	confirmBody, _ := json.Marshal(api.ConfirmUploadRequest{SizeBytes: size})
	confirmResp, err := a.authPost(
		fmt.Sprintf("%s/api/v1/artifacts/%s/confirm", a.schedulerURL, presign.ArtifactID),
		"application/json",
		bytes.NewReader(confirmBody),
	)

	if err != nil {
		fmt.Printf("[agent %s] artifact %q confirm failed: %v\n", a.id[:8], name, err)
		return fmt.Errorf("confirm: %w", err)
	}
	if confirmResp.StatusCode != http.StatusNoContent && confirmResp.StatusCode != http.StatusOK {
		fmt.Printf("[agent %s] artifact %q confirm returned HTTP %d\n", a.id[:8], name, confirmResp.StatusCode)
	}

	io.Copy(io.Discard, confirmResp.Body)
	confirmResp.Body.Close()
	fmt.Printf("[agent %s] artifact %q uploaded successfully (%d bytes)\n", a.id[:8], name, size)
	return nil
}

func (a *Agent) performCheckout(ctx context.Context, dir, projectID, commitSHA string) error {
	url := fmt.Sprintf("%s/api/v1/source/%s?commit=%s", a.schedulerURL, projectID, commitSHA)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("checkout request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.apiToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("checkout do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("checkout failed: status %d, body: %s", resp.StatusCode, string(body))
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	filesCount := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		target := filepath.Join(dir, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("create file %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("copy file %s: %w", target, err)
			}
			f.Close()
			filesCount++
		}
	}
	fmt.Printf("[agent] checkout complete: %d files extracted to %s\n", filesCount, dir)
	return nil
}

func (a *Agent) downloadFile(downloadURL, dest string) error {
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return err
	}

	// Add bearer auth for scheduler endpoints (local artifact backend).
	if u, err2 := url.Parse(downloadURL); err2 == nil && strings.HasPrefix(u.Path, "/api/v1/") {
		req.Header.Set("Authorization", "Bearer "+a.apiToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func (a *Agent) executePipelineStep(ctx context.Context, spec *api.JobSpec, jobWorkspace string) error {
	ref := spec.PipelineRef
	if ref == nil || ref.Path == "" {
		return a.reportComplete(spec, 1, 0, pipelineLog("ERROR", "pipeline step has no path"), "", false)
	}

	start := time.Now()
	logs := []api.LogEvent{{
		Timestamp: time.Now(), Level: "INFO",
		Message: fmt.Sprintf("pipeline step: %s (wait=%v)", ref.Path, ref.Wait),
	}}

	// Build the full path to the referenced pipeline file.
	pipelinePath := ref.Path
	if !filepath.IsAbs(pipelinePath) {
		// Try jobWorkspace first, fall back to a.workspaceDir
		p := filepath.Join(jobWorkspace, pipelinePath)
		if _, err := os.Stat(p); err == nil {
			pipelinePath = p
		} else {
			pipelinePath = filepath.Join(a.workspaceDir, pipelinePath)
		}
	}

	// Compile the child pipeline.
	childPipeline, err := compiler.Compile(pipelinePath)
	if err != nil {
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("compile %s: %v", ref.Path, err))...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, "", false)
	}
	logs = append(logs, pipelineLog("INFO", fmt.Sprintf("compiled child pipeline %q (%d steps)", childPipeline.Name, len(childPipeline.Steps)))...)

	steps := make([]api.StepDef, 0, len(childPipeline.Steps))
	for _, s := range childPipeline.Steps {
		env := make(map[string]string, len(s.Env)+len(ref.Variables))
		for k, v := range s.Env {
			env[k] = v
		}

		// Variables override the step's own env vars.
		for k, v := range ref.Variables {
			env[k] = v
		}
		var uploads []api.ArtifactUploadSpec
		for _, u := range s.ArtifactUploads {
			uploads = append(uploads, api.ArtifactUploadSpec{
				Path: u.Path,
				Name: u.Name,
			})
		}
		var downloads []api.ArtifactDownloadSpec
		for _, d := range s.ArtifactDownloads {
			downloads = append(downloads, api.ArtifactDownloadSpec{
				Name: d.Name,
				Dest: d.Dest,
			})
		}

		var pipelineRef *api.PipelineRef
		if s.PipelineRef != nil {
			pipelineRef = &api.PipelineRef{
				Path:             s.PipelineRef.Path,
				Wait:             s.PipelineRef.Wait,
				Variables:        s.PipelineRef.Variables,
				ArtifactsSend:    s.PipelineRef.ArtifactsSend,
				ArtifactsReceive: s.PipelineRef.ArtifactsReceive,
			}
		}

		var release *api.ReleaseConfig
		if s.Release != nil {
			release = &api.ReleaseConfig{
				Name:      s.Release.Name,
				Tag:       s.Release.Tag,
				Body:      s.Release.Body,
				Artifacts: s.Release.Artifacts,
			}
		}

		steps = append(steps, api.StepDef{
			ID:                s.ID,
			Image:             s.Image,
			Entrypoint:        s.Entrypoint,
			Command:           s.Command,
			WorkDir:           s.WorkDir,
			Env:               env,
			DependsOn:         s.DependsOn,
			Inputs:            s.Inputs,
			Timeout:           s.Timeout,
			SecretNames:       s.Secrets,
			DockerSocket:      s.DockerSocket,
			Condition:         s.Condition,
			AlwaysRun:         s.AlwaysRun,
			Type:              s.Type,
			ArtifactUploads:   uploads,
			ArtifactDownloads: downloads,
			PipelineRef:       pipelineRef,
			Release:           release,
		})
	}

	// Submit the child run.
	childRunName := fmt.Sprintf("%s → %s", spec.StepID, childPipeline.Name)
	body, _ := json.Marshal(api.SubmitRunRequest{
		PipelineName: childRunName,
		Steps:        steps,
		WorkspaceDir: a.workspaceDir,
		OrgID:        spec.OrgID,
		ProjectID:    spec.ProjectID,
		Ref:          spec.Ref,
	})
	submitResp, err := a.authPost(a.schedulerURL+"/api/v1/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("submit child run: %v", err))...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, "", false)
	}
	defer submitResp.Body.Close()

	var runResp api.SubmitRunResponse
	json.NewDecoder(submitResp.Body).Decode(&runResp)
	if runResp.RunID == "" {
		logs = append(logs, pipelineLog("ERROR", "scheduler returned empty run ID")...)
		return a.reportComplete(spec, 1, time.Since(start).Milliseconds(), logs, "", false)
	}
	logs = append(logs, pipelineLog("INFO", fmt.Sprintf("child run submitted: %s", runResp.RunID[:8]))...)

	if !ref.Wait {
		logs = append(logs, pipelineLog("INFO", "fire-and-forget — not waiting for child run")...)
		return a.reportComplete(spec, 0, time.Since(start).Milliseconds(), logs, "", false)
	}

	// Poll until the child run finishes.
	finalStatus, pollLogs := a.waitForChildRun(ctx, runResp.RunID)
	logs = append(logs, pollLogs...)

	exitCode := 0
	if finalStatus != "passed" {
		exitCode = 1
		logs = append(logs, pipelineLog("ERROR", fmt.Sprintf("child run %s: %s", runResp.RunID[:8], finalStatus))...)
	} else {
		logs = append(logs, pipelineLog("INFO", fmt.Sprintf("child run %s: passed", runResp.RunID[:8]))...)
	}

	// Copy artifacts delcared in artifacts_receive from child run into parent run
	for _, name := range ref.ArtifactsReceive {
		if err := a.bridgeArtifact(ctx, runResp.RunID, spec.RunID, spec.JobID, name); err != nil {
			logs = append(logs, pipelineLog("WARN", fmt.Sprintf("artifact %q bridge failed: %v", name, err))...)
		} else {
			logs = append(logs, pipelineLog("INFO", fmt.Sprintf("artifact %q copied from child run", name))...)
		}
	}

	return a.reportComplete(spec, exitCode, time.Since(start).Milliseconds(), logs, "", false)
}

// waitForChildRun polls the scheduler every 5 seconds until the run reaches a terminal state. Reruns the
// final status and accumulated log events.
func (a *Agent) waitForChildRun(ctx context.Context, runID string) (string, []api.LogEvent) {
	var logs []api.LogEvent
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "canceled", logs
		case <-ticker.C:
		}

		resp, err := a.authGet(fmt.Sprintf("%s/api/v1/runs/%s", a.schedulerURL, runID))
		if err != nil {
			continue
		}
		var status api.RunStatus
		json.NewDecoder(resp.Body).Decode(&status)
		resp.Body.Close()

		switch string(status.Status) {
		case "passed", "failed", "canceled":
			return string(status.Status), logs
		default:
			logs = append(logs, pipelineLog("INFO",
				fmt.Sprintf("child run %s: %s", runID[:8], status.Status))...)
		}
	}
}

// bridgeArtifact copies an artifact from a source run into the target run's artifact store.
// Used to propagate artifacts from a child pipeline back to the parent run.
func (a *Agent) bridgeArtifact(ctx context.Context, srcRunID, dstRunID, dstJobID, name string) error {
	// Download the artifact from the child run into a temp file.
	meta, err := a.getArtifact(srcRunID, name)
	if err != nil {
		return fmt.Errorf("get artifact %q from run %s: %w", name, srcRunID[:8], err)
	}

	tmp, err := os.CreateTemp("", "forge-artifact-bridge-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := a.downloadFile(meta.DownloadURL, tmp.Name()); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// Re-upload into the parent run under the same logical name.
	return a.uploadArtifact(dstRunID, dstJobID, name, tmp.Name())
}

func pipelineLog(level, msg string) []api.LogEvent {
	return []api.LogEvent{{Timestamp: time.Now(), Level: level, Message: "[pipeline] " + msg}}
}
