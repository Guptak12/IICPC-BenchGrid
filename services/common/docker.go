package common

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/client"
)

// CopyFileToContainer archives a file into a tar stream in memory and copies it to the specified container path.
func CopyFileToContainer(ctx context.Context, cli *client.Client, containerID string, destDir string, filename string, content []byte, mode int64) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	hdr := &tar.Header{
		Name:    filename,
		Mode:    mode,
		Size:    int64(len(content)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("failed to write tar header: %w", err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("failed to write tar content: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("failed to close tar writer: %w", err)
	}

	_, err := cli.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{
		DestinationPath:           destDir,
		Content:                   &buf,
		AllowOverwriteDirWithFile: true,
	})
	if err != nil {
		return fmt.Errorf("CopyToContainer failed: %w", err)
	}
	return nil
}

// CopyFileFromContainer retrieves a file from a container path and extracts it in memory.
func CopyFileFromContainer(ctx context.Context, cli *client.Client, containerID string, srcPath string) ([]byte, error) {
	res, err := cli.CopyFromContainer(ctx, containerID, client.CopyFromContainerOptions{
		SourcePath: srcPath,
	})
	if err != nil {
		return nil, fmt.Errorf("CopyFromContainer failed: %w", err)
	}
	defer res.Content.Close()

	tr := tar.NewReader(res.Content)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar stream: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg {
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, tr); err != nil {
				return nil, fmt.Errorf("failed to extract file from tar: %w", err)
			}
			return buf.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("target file not found in container tar stream")
}
