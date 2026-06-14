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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// BuildImage clones or extracts the contestant source, executes docker build with timeout,
// and returns (success, imageTag, stderr, err).
func BuildImage(ctx context.Context, s3Client *minio.Client, s3Path, githubURL, submissionID string) (bool, string, string, error) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("REGISTRY_URL") != "" {
		return buildImageWithKaniko(ctx, s3Path, githubURL, submissionID)
	}

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

func buildImageWithKaniko(ctx context.Context, s3Path, githubURL, submissionID string) (bool, string, string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return false, "", "", fmt.Errorf("failed to get in-cluster Kubernetes config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to create Kubernetes clientset: %w", err)
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	kanikoSA := os.Getenv("KANIKO_SERVICE_ACCOUNT")
	if kanikoSA == "" {
		kanikoSA = "kaniko-sa"
	}

	// 1. Determine context URL
	var contextURL string
	if s3Path != "" {
		contextURL = fmt.Sprintf("s3://%s/%s", common.S3Bucket, s3Path)
	} else if githubURL != "" {
		contextURL = githubURL
		if !strings.HasPrefix(contextURL, "git://") && strings.HasPrefix(contextURL, "https://") {
			contextURL = strings.Replace(contextURL, "https://", "git://", 1)
		}
	} else {
		return false, "", "", fmt.Errorf("invalid submission: both s3_path and github_url are empty")
	}

	registryURL := os.Getenv("REGISTRY_URL")
	if registryURL == "" {
		return false, "", "", fmt.Errorf("REGISTRY_URL environment variable is not set")
	}

	imageTag := "iicpc-contestants:contestant-" + submissionID
	remoteTag := fmt.Sprintf("%s/%s", registryURL, imageTag)
	jobName := "kaniko-build-" + submissionID

	log.Printf("[submission:%s] Scheduling Kaniko Job %s in namespace %s context=%s destination=%s...\n",
		submissionID[:8], jobName, namespace, contextURL, remoteTag)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels: map[string]string{
				"app":           "kaniko-builder",
				"submission-id": submissionID,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            pointerInt32(0),
			ActiveDeadlineSeconds:   pointerInt64(300),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: kanikoSA,
					Containers: []corev1.Container{
						{
							Name:  "kaniko",
							Image: "gcr.io/kaniko-project/executor:latest",
							Args: []string{
								"--context=" + contextURL,
								"--dockerfile=Dockerfile",
								"--destination=" + remoteTag,
								"--cache=true",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "docker-config",
									MountPath: "/kaniko/.docker",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "docker-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "kaniko-docker-config",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	_, err = clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return false, "", "", fmt.Errorf("failed to create Kaniko Job: %w", err)
	}

	defer func() {
		log.Printf("[submission:%s] Cleaning up Kaniko Job %s...\n", submissionID[:8], jobName)
		deletePolicy := metav1.DeletePropagationBackground
		_ = clientset.BatchV1().Jobs(namespace).Delete(context.Background(), jobName, metav1.DeleteOptions{
			PropagationPolicy: &deletePolicy,
		})
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)
	var succeeded bool

	for {
		select {
		case <-ctx.Done():
			return false, "", "", ctx.Err()
		case <-timeout:
			return false, "", "Build Timeout: The Kaniko build process exceeded the 5-minute limit.", nil
		case <-ticker.C:
			currentJob, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
			if err != nil {
				log.Printf("[submission:%s] Error polling Kaniko Job status: %v\n", submissionID[:8], err)
				continue
			}

			if currentJob.Status.Succeeded > 0 {
				succeeded = true
				break
			}
			if currentJob.Status.Failed > 0 {
				succeeded = false
				break
			}
		}
		if succeeded {
			break
		}
		currentJob, err := clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err == nil && currentJob.Status.Failed > 0 {
			succeeded = false
			break
		}
	}

	var logs string
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err == nil && len(pods.Items) > 0 {
		podName := pods.Items[0].Name
		req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
		podLogs, err := req.Stream(ctx)
		if err == nil {
			defer podLogs.Close()
			buf := new(bytes.Buffer)
			_, _ = io.Copy(buf, podLogs)
			logs = buf.String()
		}
	}

	if !succeeded {
		if logs == "" {
			logs = "Kaniko build job failed (no logs collected)."
		}
		return false, "", logs, nil
	}

	return true, remoteTag, "", nil
}

func pointerInt32(i int32) *int32 {
	return &i
}

func pointerInt64(i int64) *int64 {
	return &i
}

func DetectProtocol(ctx context.Context, s3Client *minio.Client, s3Path, githubURL string) string {
	protocol := "TCP_PROTOBUF"

	if s3Path != "" {
		obj, err := s3Client.GetObject(ctx, common.S3Bucket, s3Path, minio.GetObjectOptions{})
		if err == nil {
			defer obj.Close()
			zipBytes, err := io.ReadAll(obj)
			if err == nil {
				reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
				if err == nil {
					for _, file := range reader.File {
						if file.Name == "Dockerfile" {
							rc, err := file.Open()
							if err == nil {
								defer rc.Close()
								dfBytes, _ := io.ReadAll(rc)
								protocol = ParseProtocolFromDockerfile(string(dfBytes))
								break
							}
						}
					}
				}
			}
		}
	}
	return protocol
}

func ParseProtocolFromDockerfile(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "ENV ENGINE_PROTOCOL") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				val := strings.Join(parts[1:], " ")
				if strings.Contains(val, "=") {
					eqParts := strings.SplitN(val, "=", 2)
					return strings.Trim(strings.TrimSpace(eqParts[1]), "\"'")
				} else {
					if len(parts) >= 3 {
						return strings.Trim(strings.TrimSpace(parts[2]), "\"'")
					}
				}
			}
		}
	}
	return "TCP_PROTOBUF"
}

