package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"iicpc-sandbox/services/common"
	"github.com/minio/minio-go/v7"
)

// BuildImage clones or extracts the contestant source, executes docker build with timeout,
// and returns (success, imageTag, stderr, err).
func BuildImage(ctx context.Context, s3Client *minio.Client, s3Path, githubURL, submissionID string) (bool, string, string, error) {
	// Create temporary workspace directory inside the project's scratch space
	cwd, err := os.Getwd()
	if err != nil {
		return false, "", "", fmt.Errorf("failed to get current working directory: %w", err)
	}

	scratchDir := filepath.Join(cwd, "scratch")
	_ = os.MkdirAll(scratchDir, 0777)

	buildDir := filepath.Join(scratchDir, "build-"+submissionID)
	_ = os.RemoveAll(buildDir)
	if err := os.MkdirAll(buildDir, 0777); err != nil {
		return false, "", "", fmt.Errorf("failed to create build directory: %w", err)
	}
	defer func() {
		// Clean up build directory to save space
		_ = os.RemoveAll(buildDir)
	}()

	// 1. Fetch files
	if githubURL != "" {
		log.Printf("[submission:%s] Cloning github repository: %s\n", submissionID[:8], githubURL)
		cloneCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		cmd := exec.CommandContext(cloneCtx, "git", "clone", githubURL, buildDir)
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		err := cmd.Run()
		cancel()
		if err != nil {
			return false, "", fmt.Sprintf("Git clone failed: %s\n%s", err.Error(), errBuf.String()), nil
		}
	} else if s3Path != "" {
		log.Printf("[submission:%s] Downloading ZIP submission from S3...\n", submissionID[:8])
		obj, err := s3Client.GetObject(ctx, common.S3Bucket, s3Path, minio.GetObjectOptions{})
		if err != nil {
			return false, "", "", fmt.Errorf("failed to get submission ZIP from S3: %w", err)
		}
		defer obj.Close()

		zipBytes, err := io.ReadAll(obj)
		if err != nil {
			return false, "", "", fmt.Errorf("failed to read submission ZIP: %w", err)
		}

		if err := extractZip(zipBytes, buildDir); err != nil {
			return false, "", fmt.Sprintf("Failed to extract submission ZIP: %s", err.Error()), nil
		}
	} else {
		return false, "", "", fmt.Errorf("invalid submission: both github_url and s3_path are empty")
	}

	// 2. Execute Docker Build under Context Timeout (Edge Case 1: 5 minutes limit)
	imageTag := "contestant-" + submissionID
	log.Printf("[submission:%s] Triggering docker build for tag %s...\n", submissionID[:8], imageTag)

	buildCtx, cancelBuild := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelBuild()

	cmd := exec.CommandContext(buildCtx, "docker", "build", "-t", imageTag, buildDir)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	if buildCtx.Err() == context.DeadlineExceeded {
		return false, "", "Build Timeout: The Docker build process exceeded the 5-minute limit.", nil
	}

	if err != nil {
		// Build failed, return the stderr for diagnostic collection
		return false, "", stderrBuf.String() + "\n" + stdoutBuf.String(), nil
	}

	// 3. Optional Registry Push (Distributed Image Registry support)
	registryURL := os.Getenv("REGISTRY_URL")
	if registryURL != "" {
		remoteTag := fmt.Sprintf("%s/%s", registryURL, imageTag)
		log.Printf("[submission:%s] Tagging and pushing to registry: %s\n", submissionID[:8], remoteTag)

		// Tag
		tagCmd := exec.CommandContext(ctx, "docker", "tag", imageTag, remoteTag)
		if err := tagCmd.Run(); err != nil {
			return false, "", "", fmt.Errorf("failed to tag image for registry: %w", err)
		}

		// Push
		pushCmd := exec.CommandContext(ctx, "docker", "push", remoteTag)
		if err := pushCmd.Run(); err != nil {
			return false, "", "", fmt.Errorf("failed to push image to registry: %w", err)
		}

		imageTag = remoteTag
	}

	return true, imageTag, "", nil
}

// extractZip parses zip bytes and extracts files to the destination directory.
func extractZip(zipBytes []byte, destDir string) error {
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		path := filepath.Join(destDir, file.Name)
		// Prevent Zip Slip vulnerability
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(destDir)) {
			continue
		}
		if file.FileInfo().IsDir() {
			_ = os.MkdirAll(path, file.Mode())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			_ = f.Close()
			return err
		}
		_, err = io.Copy(f, rc)
		_ = f.Close()
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
