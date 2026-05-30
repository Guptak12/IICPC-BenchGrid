package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"encoding/json"
	"time"
	"net/http"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/moby/docker/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Global Docker client instance
var dockerClient *client.Client

//Docker image name for the sandbox environment and network name for container communication
const (
    SandboxImage   = "iicpc-sandbox:v1"
    SandboxNetwork = "iicpc-net"   // workers, redpanda, master
	SandboxIsolatedNet = "sandbox-net" //contestant containers only
)

// Track active sandboxes
var (
	activeSandboxes   = map[string]string{}
	activeSandboxesMu sync.Mutex
)

// Build Structs
type BuildStatus string
const (
    BuildPending   BuildStatus = "pending"
    BuildCompiling BuildStatus = "compiling"
    BuildRunning   BuildStatus = "running"
    BuildFailed    BuildStatus = "failed"
)

type BuildJob struct {
    ID          string      `json:"build_id"`
    ContestantID string     `json:"contestant_id"`
    Status      BuildStatus `json:"status"`
    SubmittedAt time.Time   `json:"submitted_at"`
    EndedAt     *time.Time  `json:"ended_at,omitempty"`
    ContainerID string      `json:"container_id,omitempty"`
    Endpoint    string      `json:"endpoint,omitempty"`
    HostPort    string      `json:"host_port,omitempty"`
    Error       string      `json:"error,omitempty"`
	JobID 	 string      `json:"job_id"`
}

var (
    buildStore   = map[string]*BuildJob{}
    buildStoreMu sync.RWMutex
)

func getBuild(id string) (*BuildJob, bool) {
    buildStoreMu.RLock()
    defer buildStoreMu.RUnlock()
    j, ok := buildStore[id]
    return j, ok
}

func replaceBuild(j *BuildJob) {
    buildStoreMu.Lock()
    defer buildStoreMu.Unlock()
    buildStore[j.ID] = j
}


func main() {

	var err error
	dockerClient, err = client.New(client.FromEnv)

	if err != nil {
		log.Fatalf("Docker connect failed: %v", err)
	}
	defer dockerClient.Close()

	// Verify image exists at startup
	_, err = dockerClient.ImageInspect(context.Background(), SandboxImage)
	if err != nil {
		log.Fatalf("Image '%s' not found. Run: docker build -f Dockerfile.sandbox -t %s .\n", SandboxImage, SandboxImage)
	}
	log.Printf("Sandbox image '%s' verified ✓\n", SandboxImage)

	if err := ensureNetwork(context.Background()); err != nil {
    log.Fatalf("Failed to create sandbox network: %v", err)
	}
	log.Printf("Sandbox network '%s' ready ✓\n", SandboxNetwork)	



	// Initialize Fiber app
	app := fiber.New(fiber.Config{
		// 10 MB limit 
		BodyLimit: 10 * 1024 * 1024, 
	})

	app.Get("/api/v1/build/:id", handleBuildStatus)
	app.Get("/api/v1/builds", handleListBuilds)
	
	app.Post("/api/v1/submit", handleSubmission)
	app.Delete("/api/v1/sandbox/:id", handleStop)


	log.Println("Orchestrator API running on port 3000...")
	log.Fatal(app.Listen(":3000"))
}

// func handleSubmission(c fiber.Ctx) error {
// 	contestantID := c.FormValue("contestant_id")
// 	if contestantID == "" {
// 		contestantID = "anonymous"
// 	}
	
// 	file, err := c.FormFile("source_code")
// 	if err != nil {
// 		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to parse source_code field"})
// 	}

// 	if filepath.Ext(file.Filename) != ".cpp" {
// 		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
// 			"error": "Only .cpp files accepted",
// 		})
// 	}

// 	hostSubmitDir, err := os.MkdirTemp(".", "submission_*")
// 	if err != nil {
// 		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create temp directory"})
// 	}

// 	absHostSubmitDir, err := filepath.Abs(hostSubmitDir)
// 	if err != nil {
// 		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to resolve absolute path"})
// 	}

// 	savePath := filepath.Join(absHostSubmitDir, "main.cpp")
// 	if err := c.SaveFile(file, savePath); err != nil {
// 		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save file"})
// 	}

// 	// Generate build ID immediately
// buildID := uuid.New().String()

// log.Printf("==== ORCHESTRATOR EXTRACTED ID: '%s' ====", contestantID)

// replaceBuild(&BuildJob{
//     ID:          buildID,
// 	ContestantID: contestantID,
//     Status:      BuildPending,
//     SubmittedAt: time.Now(),
// })

// // Launch compilation in background — return immediately
// go func() {
//     defer func() {
//         if r := recover(); r != nil {
//             now := time.Now()
//             replaceBuild(&BuildJob{
//                 ID:      buildID,
//                 Status:  BuildFailed,
//                 Error:   fmt.Sprintf("panic: %v", r),
//                 EndedAt: &now,
//             })
//         }
//     }()

//     replaceBuild(&BuildJob{
//         ID:          buildID,
//         Status:      BuildCompiling,
//         SubmittedAt: time.Now(),
//     })

//     containerID, endpoint, err := runSandbox(absHostSubmitDir)
//     now := time.Now()

//     if err != nil {
//         os.RemoveAll(absHostSubmitDir)
//         replaceBuild(&BuildJob{
//             ID:      buildID,
//             Status:  BuildFailed,
//             Error:   err.Error(),
//             EndedAt: &now,
//         })
//         return
// 	}


// 	activeSandboxesMu.Lock()
// 	activeSandboxes[containerID] = absHostSubmitDir
// 	activeSandboxesMu.Unlock()

// 	// 1. Extract it safely here, while the HTTP context is still alive
// 	contestantID := c.FormValue("contestant_id")
// 	if contestantID == "" {
// 		contestantID = "anonymous" // Fallback just in case
// 	}

// 	replaceBuild(&BuildJob{
//         ID:          buildID,
//         Status:      BuildRunning,
//         ContainerID: containerID[:12],
//         Endpoint:    endpoint,
//         EndedAt:     &now,
//     })
// 	// 2. AUTO-TRIGGER THE OFFICIAL EXAM RIGHT HERE
// 		go func() {
// 			jobID, err := triggerOfficialRun(buildID, contestantID, endpoint)
// 			if err != nil {
// 				log.Printf("[job:%s] auto-trigger failed: %v\n", buildID[:8], err)
// 			} else {
// 				// This log will now print the JobID!
// 				log.Printf("[job:%s] official exam triggered for %s (JobID: %s)\n", buildID[:8], contestantID, jobID)
				
// 				// Save the JobID into the Orchestrator's state so the bash script can fetch it!
// 				buildStoreMu.Lock()
// 				if build, exists := buildStore[buildID]; exists {
// 					build.JobID = jobID
// 				}
// 				buildStoreMu.Unlock()
// 			}
// 		}()
// }()
// // Return 202 immediately — client polls /api/v1/build/:id
// return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
//     "build_id": buildID,
//     "status":   "pending",
//     "poll":     fmt.Sprintf("/api/v1/build/%s", buildID),
// })
	
// }

func handleSubmission(c fiber.Ctx) error {
	// 1. EXTRACT DATA ON MAIN THREAD (Safe from Fiber recycling)
	contestantID := c.FormValue("contestant_id")
	if contestantID == "" {
		contestantID = "anonymous"
	}

	file, err := c.FormFile("source_code")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing source_code"})
	}

	buildID := uuid.New().String()
	
	// Use a local 'submissions' folder in the current working directory instead of the OS Temp dir
	cwd, _ := os.Getwd()
	absHostSubmitDir := filepath.Join(cwd, "submissions", "iicpc_"+buildID)
	os.MkdirAll(absHostSubmitDir, 0755)

	// 2. SAVE FILE ON MAIN THREAD 
	// (Never do this inside the go func!)
	if err := c.SaveFile(file, filepath.Join(absHostSubmitDir, "main.cpp")); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to save file"})
	}

	// 3. INITIALIZE THE JOB SAFELY
	buildStoreMu.Lock()
	buildStore[buildID] = &BuildJob{
		ID:           buildID,
		ContestantID: contestantID,
		Status:       BuildCompiling,
		SubmittedAt:  time.Now(),
	}
	buildStoreMu.Unlock()

	// 4. LAUNCH ASYNC GOROUTINE
	// Pass strings explicitly as arguments to avoid closure leaks
	go func(bID, cID, hostDir string) {
		// Panic Recovery (Updates instead of Overwriting)
		defer func() {
			if r := recover(); r != nil {
				now := time.Now()
				buildStoreMu.Lock()
				if b, ok := buildStore[bID]; ok {
					b.Status = BuildFailed
					b.Error = fmt.Sprintf("panic: %v", r)
					b.EndedAt = &now
				}
				buildStoreMu.Unlock()
			}
		}()

		// Compile and Boot Sandbox
		containerID, endpoint, err := runSandbox(hostDir)
		now := time.Now()

		if err != nil {
			os.RemoveAll(hostDir)
			buildStoreMu.Lock()
			if b, ok := buildStore[bID]; ok {
				b.Status = BuildFailed
				b.Error = err.Error()
				b.EndedAt = &now
			}
			buildStoreMu.Unlock()
			return
		}

		activeSandboxesMu.Lock()
		activeSandboxes[containerID] = hostDir
		activeSandboxesMu.Unlock()

		// Update to Running
		buildStoreMu.Lock()
		if b, ok := buildStore[bID]; ok {
			b.Status = BuildRunning
			b.ContainerID = containerID[:12]
			b.Endpoint = endpoint
			b.EndedAt = &now
		}
		buildStoreMu.Unlock()

		// Trigger Master Node
		jobID, err := triggerOfficialRun(bID, cID, endpoint)
		if err != nil {
			// Trigger failed — container will never be used. Clean up immediately.
    		log.Printf("[job:%s] auto-trigger failed: %v — cleaning up sandbox now\n", bID[:8], err)
    		cleanupSandbox(containerID, hostDir)
    return
		}
		
		log.Printf("[job:%s] official exam triggered for %s (JobID: %s)\n", bID[:8], cID, jobID)
			
		buildStoreMu.Lock()
		if b, ok := buildStore[bID]; ok {
			b.JobID = jobID
		}
		buildStoreMu.Unlock()

	// Launch the cleanup watcher — runs independently until job completes
	go watchAndCleanup(jobID, containerID, hostDir)

	}(buildID, contestantID, absHostSubmitDir)

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"build_id": buildID,
		"status":   "pending",
		"poll":     fmt.Sprintf("/api/v1/build/%s", buildID),
	})
}

func runSandbox(hostSubmitDir string) (string, string, error) {

	ctx := context.Background()
	
	// Unique container name — used as internal DNS hostname
    containerName := "sandbox-" + uuid.New().String()[:8]

    // Compile step — no network needed
	if err := compileCode(ctx, hostSubmitDir); err != nil {
		return "", "", err
	}

	// Step 2 — get a free port on the host
	// hostPort, err := getFreePort()
	// if err != nil {
	// 	return "", "", fmt.Errorf("no free port: %v", err)
	// }

	containerID, err := createContainer(
		ctx,
		[]string{"/usr/src/app"},
		hostSubmitDir,
		containerName,
	)

	if err != nil {
		return "", "", err
	}

	if _, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
		return "", "", fmt.Errorf("container start failed: %v", err)
	}

	 
	// Wait until contestant's server is accepting connections
	// if err := waitForSandboxReady(ctx, containerID, containerName, 10*time.Second); err != nil {
	// 	dockerClient.ContainerStop(ctx, containerID, client.ContainerStopOptions{})
	// 	dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
	// 	return "", "", fmt.Errorf("server did not start: %v", err)
	// }

	// --- UPDATED PRODUCTION HEALTH CHECK FOR DOCKER DESKTOP COMPATIBILITY ---
    // Give the C++ server 1.5 seconds to initialize or crash if it has a startup error (e.g., segfault)
    time.Sleep(1500 * time.Millisecond)

    // Query the Docker Daemon directly for the container state
    inspect, err := dockerClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
    if err != nil {
        dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
        return "", "", fmt.Errorf("failed to inspect container: %v", err)
    }

    // Check if the binary died instantly on boot
    if !inspect.Container.State.Running {
        dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
        return "", "", fmt.Errorf("contestant server crashed immediately upon startup")
    }
    // -----------------------------------------------------------------------
	// Get IP again for the endpoint
info, _ := dockerClient.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
netInfo,ok := info.Container.NetworkSettings.Networks[SandboxIsolatedNet]
if !ok || netInfo == nil {
    dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
    return "", "", fmt.Errorf("container not attached to %s — check network exists", SandboxIsolatedNet)
}

// netInfo.IPAddress is netip.Addr, not string — use .IsValid()
if !netInfo.IPAddress.IsValid() {
    dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
    return "", "", fmt.Errorf("container on %s but has no IP assigned yet", SandboxIsolatedNet)
}
endpoint := fmt.Sprintf("ws://%s:8080/ws", netInfo.IPAddress.String())
return containerID, endpoint, nil
	// // Endpoint uses internal DNS — only reachable from iicpc-net
    // endpoint := fmt.Sprintf("ws://%s:8080/ws", containerName)
	// return containerID, endpoint, nil
}	

func handleStop(c fiber.Ctx) error {
	id := c.Params("id")

	if id == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing container ID"})
	}

	_,err := dockerClient.ContainerStop(context.Background(), id, client.ContainerStopOptions{})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	dockerClient.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{Force: true})

	activeSandboxesMu.Lock()
	if dir, ok := activeSandboxes[id]; ok {
		os.RemoveAll(dir)
		delete(activeSandboxes, id)
	}
	activeSandboxesMu.Unlock()

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "stopped"})
}

func createContainer(ctx context.Context, cmd []string, hostSubmitDir string, containerName string) (string, error) {
	pidsLimit := int64(2048) // Increased to support high concurrency testing

	isRuntime := containerName != ""
	 // Threat 1 fix: runtime containers get read-only mount.
    // Compile containers need write access to produce the /usr/src/app binary.
    bind := fmt.Sprintf("%s:/usr/src:ro", hostSubmitDir)
    if !isRuntime {
        bind = fmt.Sprintf("%s:/usr/src", hostSubmitDir)
    }

    // Threat 2 fix: runtime containers get seccomp profile blocking
    // fork/exec syscalls. Compile containers are exempt — g++ needs them.
    securityOpts := []string{"no-new-privileges"}
    if isRuntime {
        securityOpts = append(securityOpts, "seccomp="+sandboxSeccompProfile)
    }

	config := &container.Config{
		Image: SandboxImage,
		Cmd:   cmd,
		Tty:   false,
	}
	if containerName != "" {
    config.Hostname = containerName
}


	hostConfig := &container.HostConfig{
		Binds: []string{bind},
		Resources: container.Resources{
			Memory:    256 * 1024 * 1024,
			NanoCPUs:  int64(1 * 1e9),
			PidsLimit: &pidsLimit,
		},
		CapDrop:      []string{"ALL"},
		SecurityOpt:  securityOpts,
	}

	// Attach to sandbox network
    var networkConfig *network.NetworkingConfig
	if containerName != "" {
    networkConfig = &network.NetworkingConfig{
        EndpointsConfig: map[string]*network.EndpointSettings{
            SandboxIsolatedNet: {
                Aliases: []string{containerName},
            },
        },
    }
}else{
	hostConfig.NetworkMode = "none"
}

	resp, err := dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
		NetworkingConfig: networkConfig,
		Name: containerName,
	})
	if err != nil {
		return "", fmt.Errorf("container create failed: %v", err)
	}

	return resp.ID, nil
}


func compileCode(ctx context.Context, hostSubmitDir string) error {
	containerID, err := createContainer(
		ctx,
		[]string{"g++",
			"-O3",
			"-I/core",
			"/core/hidden_server.cpp",
			"/usr/src/main.cpp",
			"-o", "/usr/src/app",
			"-lcrypto",
			"-lpthread",
			"-std=c++17",
		},
		hostSubmitDir,
		"",// no name needed — compile container has no network
	)
	if err != nil {
		return err
	}
	defer dockerClient.ContainerRemove(context.Background(), containerID, client.ContainerRemoveOptions{Force: true})

	if _, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("compile start failed: %v", err)
	}


	containerResult := dockerClient.ContainerWait(ctx, containerID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	select {
	case err := <-containerResult.Error:
		return fmt.Errorf("compile wait error: %v", err)
	case result := <-containerResult.Result:
		if result.StatusCode != 0 {
			// Fetch compiler errors
			logs, err := dockerClient.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
				ShowStderr: true,
				ShowStdout: true,
			})
			if err != nil {
				return fmt.Errorf("compilation failed (could not fetch errors)")
			}
			defer logs.Close()
			var stdoutBuf, stderrBuf bytes.Buffer
			stdcopy.StdCopy(&stdoutBuf, &stderrBuf, logs)
			return fmt.Errorf("compilation failed:\n%s", stderrBuf.String())
		}
	case <-time.After(30 * time.Second):
		return fmt.Errorf("compilation timed out")
	}

	return nil
}
func ensureNetwork(ctx context.Context) error {
	 for _, net := range []struct{ name string; internal bool }{
        {SandboxNetwork,     true},
        {SandboxIsolatedNet, true},
    } {
    // Check if network already exists
    _, err := dockerClient.NetworkInspect(ctx, net.name, client.NetworkInspectOptions{})
	if err == nil {
		log.Printf("Network '%s' already exists ✓\n", net.name)
            continue // ← was return nil — that skipped the second network entirely
	}
    // 2. If the error is anything OTHER than "Not Found", panic
	if client.IsErrConnectionFailed(err) {
		return fmt.Errorf("network inspect failed: %v", err)
	}
    // 3. Create the isolated bridge network
	_, err = dockerClient.NetworkCreate(ctx, net.name, client.NetworkCreateOptions{
		Driver:   "bridge",
		Internal: true, // Completely cuts off internet access to the sandbox
	})
	if err != nil {
            return fmt.Errorf("create %s failed: %v", net.name, err)
        }
        log.Printf("Network '%s' created ✓\n", net.name)
}
	return nil
}

func handleBuildStatus(c fiber.Ctx) error {
    id := c.Params("id")
    job, ok := getBuild(id)
    if !ok {
        return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "build not found"})
    }
    return c.JSON(job)
}

func handleListBuilds(c fiber.Ctx) error {
    buildStoreMu.RLock()
    defer buildStoreMu.RUnlock()
    builds := make([]*BuildJob, 0, len(buildStore))
    for _, j := range buildStore {
        builds = append(builds, j)
    }
    return c.JSON(builds)
}


func waitForSandboxReady(ctx context.Context, containerID string, _ string, timeout time.Duration) error {
    // Temporarily inspect container to get its internal IP on iicpc-net
    info, err := dockerClient.ContainerInspect(ctx, containerID,client.ContainerInspectOptions{})
    if err != nil {
        return err
    }
    
    netInfo, ok := info.Container.NetworkSettings.Networks[SandboxNetwork]
    if !ok {
        return fmt.Errorf("container not on %s network", SandboxNetwork)
    }
    
    // Use the container's IP directly — host can reach bridge IPs
    internalIP := netInfo.IPAddress
    addr := fmt.Sprintf("%s:8080", internalIP)
    
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        conn, err := net.DialTimeout("tcp", addr, time.Second)
        if err == nil {
            conn.Close()
            return nil
        }
        time.Sleep(500 * time.Millisecond)
    }
    return fmt.Errorf("timeout waiting for %s", addr)
}
// masterAddr reads MASTER_ADDR env var
func masterAddr() string {
    if v := os.Getenv("MASTER_ADDR"); v != "" {
        return v
    }
    return "http://localhost:4000"
}

// triggerOfficialRun fires the standardized benchmark against the master.
// Called by the orchestrator once the sandbox is confirmed running.
func triggerOfficialRun(buildID, contestantID, endpoint string) (string, error) {
	payload := map[string]interface{}{
		"contestant_id":  contestantID,
		"endpoint":       endpoint,
		 // All values read from env — production defaults baked in,
        // overridable for local testing without touching code
        "num_bots":       getEnvInt("EXAM_NUM_BOTS", 500),
        "orders_per_bot": getEnvInt("EXAM_ORDERS_PER_BOT", 1000),
        "rate_per_sec":   getEnvFloat("EXAM_RATE_PER_SEC", 100.0),
        "seed":           getEnvInt64("EXAM_SEED", 42424242),
		"mid_price":      100.0,
		"spread":         0.10,
		"strategy_mix": map[string]float64{
			"market_maker":    0.4,
			"momentum_trader": 0.3,
			"noise_trader":    0.3,
		},
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(masterAddr()+"/run", "application/json", bytes.NewBuffer(data))
	if err != nil {
		return "", fmt.Errorf("master unreachable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("master rejected run: status %d", resp.StatusCode)
	}

	// Capture the JobID returned by the Master node
	var result struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.JobID, nil
}

// cleanupSandbox stops the container, removes it, and deletes the submission dir.
// Safe to call multiple times — Docker and os.RemoveAll are both idempotent.
func cleanupSandbox(containerID, hostDir string) {
    ctx := context.Background()
    dockerClient.ContainerStop(ctx, containerID, client.ContainerStopOptions{})
    dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})

    activeSandboxesMu.Lock()
    delete(activeSandboxes, containerID)
    activeSandboxesMu.Unlock()

    os.RemoveAll(hostDir)
    log.Printf("[gc] removed container %s and submission dir\n", containerID[:12])
}

// pollJobStatus asks the master for the current status of a job.
// Returns "" on any error so the caller retries next tick.
func pollJobStatus(jobID string) string {
    c := &http.Client{Timeout: 5 * time.Second}
    resp, err := c.Get(masterAddr() + "/status/" + jobID)
    if err != nil {
        return ""
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return ""
    }

    var job struct {
        Status string `json:"status"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
        return ""
    }
    return job.Status
}

// watchAndCleanup polls the master every 15 seconds until the job reaches a
// terminal state, then tears down the sandbox container and submission dir.
// A hard 20-minute deadline ensures cleanup happens even if the master dies.
func watchAndCleanup(jobID, containerID, hostDir string) {
    ticker  := time.NewTicker(15 * time.Second)
    deadline := time.NewTimer(20 * time.Minute)
    defer ticker.Stop()
    defer deadline.Stop()

    for {
        select {
        case <-deadline.C:
            log.Printf("[gc] job %s hit 20m deadline — forcing cleanup\n", jobID[:8])
            cleanupSandbox(containerID, hostDir)
            return

        case <-ticker.C:
            status := pollJobStatus(jobID)
            switch status {
            case "completed", "aborted":
                log.Printf("[gc] job %s finished (%s) — cleaning up sandbox\n", jobID[:8], status)
                cleanupSandbox(containerID, hostDir)
                return
            case "":
                // Master unreachable — log and retry next tick
                log.Printf("[gc] job %s: master unreachable, will retry\n", jobID[:8])
            default:
                // "pending" or "running" — still active, keep polling
            }
        }
    }
}

// helper functions — add these near the top of main.go
func getEnvInt(key string, fallback int) int {
    if v := os.Getenv(key); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            return n
        }
    }
    return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
    if v := os.Getenv(key); v != "" {
        if f, err := strconv.ParseFloat(v, 64); err == nil {
            return f
        }
    }
    return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
    if v := os.Getenv(key); v != "" {
        if n, err := strconv.ParseInt(v, 10, 64); err == nil {
            return n
        }
    }
    return fallback
}