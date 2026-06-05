package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"iicpc-sandbox/services/common"

	"github.com/moby/docker/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// CompileCode runs g++ inside a resource-constrained compilation container.
// Returns (bool, string, []byte, error) where (success, compiler_stderr, binary_bytes, err).
func CompileCode(ctx context.Context, dockerClient *client.Client, sourceCode []byte) (bool, string, []byte, error) {
	containerID, err := createCompileContainer(ctx, dockerClient)
	if err != nil {
		return false, "", nil, fmt.Errorf("compile container creation failed: %v", err)
	}

	defer func() {
		_, _ = dockerClient.ContainerRemove(context.Background(), containerID, client.ContainerRemoveOptions{Force: true})
	}()

	// Copy main.cpp to container filesystem
	err = common.CopyFileToContainer(ctx, dockerClient, containerID, "/usr/src", "main.cpp", sourceCode, 0666)
	if err != nil {
		return false, "", nil, fmt.Errorf("failed to copy source code: %v", err)
	}

	// Start container
	if _, err := dockerClient.ContainerStart(ctx, containerID, client.ContainerStartOptions{}); err != nil {
		return false, "", nil, fmt.Errorf("compile start failed: %v", err)
	}

	// Wait for compilation to complete (with a hard 30-second timeout)
	containerResult := dockerClient.ContainerWait(ctx, containerID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	select {
	case err := <-containerResult.Error:
		return false, "", nil, fmt.Errorf("compile wait error: %v", err)
	case result := <-containerResult.Result:
		if result.StatusCode != 0 {
			// Fetch compiler stderr logs
			logs, err := dockerClient.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
				ShowStderr: true,
				ShowStdout: true,
			})
			if err != nil {
				return false, "", nil, fmt.Errorf("compilation failed (could not fetch errors: %v)", err)
			}
			defer logs.Close()

			var stdoutBuf, stderrBuf bytes.Buffer
			_, _ = stdcopy.StdCopy(&stdoutBuf, &stderrBuf, logs)
			return false, stderrBuf.String(), nil, nil
		}
	case <-time.After(30 * time.Second):
		return false, "", nil, fmt.Errorf("compilation timed out after 30 seconds")
	}

	// Retrieve compiled binary from container filesystem
	binaryBytes, err := common.CopyFileFromContainer(ctx, dockerClient, containerID, "/usr/src/app")
	if err != nil {
		return false, "", nil, fmt.Errorf("failed to copy binary from container: %v", err)
	}

	return true, "", binaryBytes, nil
}

func createCompileContainer(ctx context.Context, dockerClient *client.Client) (string, error) {
	pidsLimit := int64(2048)

	config := &container.Config{
		Image: common.CompileImage,
		Cmd: []string{
			"g++",
			"-O3",
			"-I/core",
			"/core/hidden_server.cpp",
			"/usr/src/main.cpp",
			"-o", "/usr/src/app",
			"-lcrypto",
			"-lpthread",
			"-std=c++17",
		},
		Tty:  false,
		User: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
	}

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			Memory:    256 * 1024 * 1024, // 256MB limit
			NanoCPUs:  int64(1 * 1e9),     // 1 CPU
			PidsLimit: &pidsLimit,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		NetworkMode: "none", // Completely isolated, no network access during compilation
	}

	resp, err := dockerClient.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
		Name:       "",
	})
	if err != nil {
		return "", err
	}

	return resp.ID, nil
}
