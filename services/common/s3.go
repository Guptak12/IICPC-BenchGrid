package common

import (
	"context"
	"fmt"
	"log"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	S3Endpoint  = getEnv("S3_ENDPOINT", "localhost:9000")
	S3AccessKey = getEnv("S3_ACCESS_KEY", "minioadmin")
	S3SecretKey = getEnv("S3_SECRET_KEY", "minioadmin")
	S3Bucket    = getEnv("S3_BUCKET", "submissions")
	S3UseSSL    = getEnv("S3_USE_SSL", "false") == "true"
)

func GetS3Client() (*minio.Client, error) {
	cli, err := minio.New(S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(S3AccessKey, S3SecretKey, ""),
		Secure: S3UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %w", err)
	}
	return cli, nil
}

func EnsureS3Bucket(ctx context.Context, cli *minio.Client) error {
	exists, err := cli.BucketExists(ctx, S3Bucket)
	if err != nil {
		return fmt.Errorf("failed to check if S3 bucket exists: %w", err)
	}
	if !exists {
		err = cli.MakeBucket(ctx, S3Bucket, minio.MakeBucketOptions{})
		if err != nil {
			return fmt.Errorf("failed to create S3 bucket: %w", err)
		}
		log.Printf("Created S3 bucket '%s' ✓\n", S3Bucket)
	}
	return nil
}
