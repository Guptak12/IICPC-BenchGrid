package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

// normalizeZipToTarGz converts a zip archive (typically from macOS Finder "Compress")
// into a gzip-compressed tar archive that Kaniko can use as an S3 build context.
//
// It performs three normalizations:
//  1. Strips a single top-level directory wrapper (e.g. "my_submission/Dockerfile" → "Dockerfile")
//  2. Removes macOS metadata entries (__MACOSX/, .DS_Store)
//  3. Converts from zip to tar.gz (Kaniko requires tar.gz for S3 context; zip gives "gzip: invalid header")
//
// Example input zip:
//
//	go_optimized/
//	  Dockerfile
//	  main.go
//	__MACOSX/
//	  ...
//
// Returns a tar.gz whose root contains Dockerfile and main.go directly.
func normalizeZipToTarGz(data []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid zip: %w", err)
	}

	// Filter out __MACOSX and .DS_Store entries
	var realFiles []*zip.File
	for _, f := range r.File {
		n := f.Name
		if strings.HasPrefix(n, "__MACOSX/") || strings.HasSuffix(n, ".DS_Store") {
			continue
		}
		realFiles = append(realFiles, f)
	}
	if len(realFiles) == 0 {
		return nil, fmt.Errorf("zip archive is empty after removing metadata")
	}

	// Detect a single common root directory prefix so we can strip it.
	prefix := detectRootPrefix(realFiles)

	// Write tar.gz
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for _, f := range realFiles {
		name := f.Name
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" || name == "/" {
			continue // skip the root dir entry itself
		}

		fi := f.FileInfo()

		hdr := &tar.Header{
			Name:     name,
			Size:     fi.Size(),
			Mode:     int64(fi.Mode()),
			ModTime:  fi.ModTime(),
			Typeflag: tar.TypeReg,
		}
		if fi.IsDir() {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
			// Ensure directory name ends with /
			if !strings.HasSuffix(name, "/") {
				hdr.Name = name + "/"
			}
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("tar header error for %s: %w", name, err)
		}

		if !fi.IsDir() {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("failed to open zip entry %s: %w", f.Name, err)
			}
			if _, err := io.Copy(tw, rc); err != nil {
				rc.Close()
				return nil, fmt.Errorf("failed to copy zip entry %s: %w", f.Name, err)
			}
			rc.Close()
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize tar: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize gzip: %w", err)
	}

	return buf.Bytes(), nil
}

// detectRootPrefix returns the single common directory prefix if every non-trivial
// file in the archive shares the same top-level folder, otherwise returns "".
func detectRootPrefix(files []*zip.File) string {
	var prefix string
	for _, f := range files {
		// Skip directory entries — the root dir itself (e.g. "go_optimized/")
		// splits into ["go_optimized", ""] and would falsely abort detection.
		if f.FileInfo().IsDir() {
			continue
		}
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			// A file sitting directly at root — no common prefix possible
			return ""
		}
		candidate := parts[0] + "/"
		if prefix == "" {
			prefix = candidate
		} else if prefix != candidate {
			return "" // multiple top-level directories
		}
	}
	return prefix
}
