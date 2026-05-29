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
	"time"

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
    Status      BuildStatus `json:"status"`
    SubmittedAt time.Time   `json:"submitted_at"`
    EndedAt     *time.Time  `json:"ended_at,omitempty"`
    ContainerID string      `json:"container_id,omitempty"`
    Endpoint    string      `json:"endpoint,omitempty"`
    HostPort    string      `json:"host_port,omitempty"`
    Error       string      `json:"error,omitempty"`
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

func handleSubmission(c fiber.Ctx) error {
	file, err := c.FormFile("source_code")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Failed to parse source_code field"})
	}

	if filepath.Ext(file.Filename) != ".cpp" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Only .cpp files accepted",
		})
	}

	hostSubmitDir, err := os.MkdirTemp(".", "submission_*")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to create temp directory"})
	}

	absHostSubmitDir, err := filepath.Abs(hostSubmitDir)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to resolve absolute path"})
	}

	savePath := filepath.Join(absHostSubmitDir, "main.cpp")
	if err := c.SaveFile(file, savePath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save file"})
	}

	// Generate build ID immediately
buildID := uuid.New().String()

replaceBuild(&BuildJob{
    ID:          buildID,
    Status:      BuildPending,
    SubmittedAt: time.Now(),
})

// Launch compilation in background — return immediately
go func() {
    defer func() {
        if r := recover(); r != nil {
            now := time.Now()
            replaceBuild(&BuildJob{
                ID:      buildID,
                Status:  BuildFailed,
                Error:   fmt.Sprintf("panic: %v", r),
                EndedAt: &now,
            })
        }
    }()

    replaceBuild(&BuildJob{
        ID:          buildID,
        Status:      BuildCompiling,
        SubmittedAt: time.Now(),
    })

    containerID, endpoint, err := runSandbox(absHostSubmitDir)
    now := time.Now()

    if err != nil {
        os.RemoveAll(absHostSubmitDir)
        replaceBuild(&BuildJob{
            ID:      buildID,
            Status:  BuildFailed,
            Error:   err.Error(),
            EndedAt: &now,
        })
        return
	}


	activeSandboxesMu.Lock()
	activeSandboxes[containerID] = absHostSubmitDir
	activeSandboxesMu.Unlock()

	replaceBuild(&BuildJob{
        ID:          buildID,
        Status:      BuildRunning,
        ContainerID: containerID[:12],
        Endpoint:    endpoint,
        EndedAt:     &now,
    })
}()
// Return 202 immediately — client polls /api/v1/build/:id
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

	config := &container.Config{
		Image: SandboxImage,
		Cmd:   cmd,
		Tty:   false,
	}
	if containerName != "" {
    config.Hostname = containerName
}


	hostConfig := &container.HostConfig{
		Binds: []string{fmt.Sprintf("%s:/usr/src", hostSubmitDir)},
		Resources: container.Resources{
			Memory:    256 * 1024 * 1024,
			NanoCPUs:  int64(1 * 1e9),
			PidsLimit: &pidsLimit,
		},
		CapDrop:      []string{"ALL"},
		SecurityOpt:  []string{"no-new-privileges"},
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
    _, err := dockerClient.NetworkInspect(ctx, SandboxNetwork, client.NetworkInspectOptions{})
	if err == nil {
		return nil // Network already exists
	}
    // 2. If the error is anything OTHER than "Not Found", panic
	if client.IsErrConnectionFailed(err) {
		return fmt.Errorf("network inspect failed: %v", err)
	}
    // 3. Create the isolated bridge network
	_, err = dockerClient.NetworkCreate(ctx, SandboxNetwork, client.NetworkCreateOptions{
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