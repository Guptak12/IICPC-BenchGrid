package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/moby/docker/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
    "net"
    "net/netip"
)
const SandboxImage = "iicpc-sandbox:v1"

var dockerClient *client.Client

var activeSandboxes = map[string]string{}


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



	// Initialize Fiber app
	app := fiber.New(fiber.Config{
		// 10 MB limit 
		BodyLimit: 10 * 1024 * 1024, 
	})

	
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

	containerID, hostPort, err := runSandbox(absHostSubmitDir)
	if err != nil {
		os.RemoveAll(hostSubmitDir)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"status": "failed",
			"error":  err.Error(),
		})
	}

	activeSandboxes[containerID] = absHostSubmitDir

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"status":       "running",
		"container_id": containerID[:12],
		"host_port":    hostPort,
		"endpoint":     fmt.Sprintf("http://localhost:%s", hostPort),
		"message":      "Sandbox is live. Send orders to the endpoint.",
	})

	
}

func runSandbox(hostSubmitDir string) (string, string, error) {

	ctx := context.Background()
	
	if err := compileCode(ctx, hostSubmitDir); err != nil {
		return "", "", err
	}

	// Step 2 — get a free port on the host
	hostPort, err := getFreePort()
	if err != nil {
		return "", "", fmt.Errorf("no free port: %v", err)
	}

	containerID, err := createContainer(
		ctx,
		[]string{"/usr/src/app"},
		hostSubmitDir,
		map[network.Port][]network.PortBinding{
			network.MustParsePort("8080/tcp"): {{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: hostPort}},
		},
	)
	if err != nil {
		return "", "", err
	}

	if _, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
		return "", "", fmt.Errorf("container start failed: %v", err)
	}

	// Wait until contestant's server is accepting connections
	if err := waitForPort(hostPort, 10*time.Second); err != nil {
		dockerClient.ContainerStop(ctx, containerID, client.ContainerStopOptions{})
		dockerClient.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true})
		return "", "", fmt.Errorf("server did not start in time: %v", err)
	}

	return containerID, hostPort, nil
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

	if dir, ok := activeSandboxes[id]; ok {
		os.RemoveAll(dir)
		delete(activeSandboxes, id)
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{"status": "stopped"})
}

func createContainer(ctx context.Context, cmd []string, hostSubmitDir string, portBindings map[network.Port][]network.PortBinding) (string, error) {
	pidsLimit := int64(100)

	config := &container.Config{
		Image: SandboxImage,
		Cmd:   cmd,
		Tty:   false,
	}

	if portBindings != nil {
		config.ExposedPorts = network.PortSet{
			network.MustParsePort("8080/tcp"): {},
		}
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
		PortBindings: portBindings,
	}

	resp, err := dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
	})
	if err != nil {
		return "", fmt.Errorf("container create failed: %v", err)
	}

	return resp.ID, nil
}


func compileCode(ctx context.Context, hostSubmitDir string) error {
	containerID, err := createContainer(
		ctx,
		[]string{"g++", "/usr/src/main.cpp", "-o", "/usr/src/app", "-lssl", "-lcrypto","-lpthread", "-std=c++17"},
		hostSubmitDir,
		nil, // no port bindings needed for compilation
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
func getFreePort() (string, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return "", err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return "", err
	}
	defer l.Close()
	return fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port), nil
}

func waitForPort(port string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "localhost:"+port, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for port %s", port)
}
