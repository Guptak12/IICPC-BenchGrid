package common

import (
	"context"
	"fmt"
	"log"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	S3Endpoint = GetEnv("S3_ENDPOINT", "s3.amazonaws.com")
	S3Bucket   = GetEnv("S3_BUCKET", "submissions")
	S3UseSSL   = GetEnv("S3_USE_SSL", "true") == "true"
	S3Region   = GetEnv("AWS_REGION", GetEnv("AWS_DEFAULT_REGION", "us-east-1"))
)

// GetS3Client returns a minio client configured for either:
//   - Local dev (MinIO): uses S3_ACCESS_KEY / S3_SECRET_KEY env vars
//   - AWS EKS (production): uses the AWS SDK default credential chain, which supports:
//     • Static env vars (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY)
//     • IRSA / Web Identity Token (AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE)
//     • EC2/ECS instance metadata
func GetS3Client() (*minio.Client, error) {
	var creds *credentials.Credentials

	accessKey := os.Getenv("S3_ACCESS_KEY")
	secretKey := os.Getenv("S3_SECRET_KEY")

	if accessKey != "" && accessKey != "minioadmin" {
		// Local dev path: explicit MinIO or direct static AWS credentials
		log.Printf("S3: using static credentials (local dev mode)")
		creds = credentials.NewStaticV4(accessKey, secretKey, "")
	} else {
		// AWS production path: resolve credentials via AWS SDK default chain.
		// This handles IRSA (AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE → STS
		// AssumeRoleWithWebIdentity) transparently, as well as env var credentials
		// and EC2 instance profile.
		log.Printf("S3: resolving credentials via AWS SDK default chain (region: %s)", S3Region)
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
			awsconfig.WithRegion(S3Region),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to load AWS config: %w", err)
		}
		awsCreds, err := cfg.Credentials.Retrieve(context.Background())
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve AWS credentials (check IRSA role binding): %w", err)
		}
		log.Printf("S3: credentials resolved (source: %s)", awsCreds.Source)
		// Pass the resolved temporary credentials (including SessionToken for IRSA) to minio.
		creds = credentials.NewStaticV4(
			awsCreds.AccessKeyID,
			awsCreds.SecretAccessKey,
			awsCreds.SessionToken,
		)
	}

	cli, err := minio.New(S3Endpoint, &minio.Options{
		Creds:  creds,
		Secure: S3UseSSL,
		Region: S3Region,
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
