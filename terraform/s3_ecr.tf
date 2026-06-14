# S3 Bucket for Submissions
resource "aws_s3_bucket" "submissions" {
  bucket        = "${var.cluster_name}-submissions-bucket"
  force_destroy = true

  tags = {
    Name = "${var.cluster_name}-submissions"
  }
}

resource "aws_s3_bucket_public_access_block" "submissions_acl" {
  bucket = aws_s3_bucket.submissions.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# ECR Repositories for Platform Services
resource "aws_ecr_repository" "gateway" {
  name                 = "iicpc-gateway"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = {
    Name = "iicpc-gateway"
  }
}

resource "aws_ecr_repository" "compiler" {
  name                 = "iicpc-compiler"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = {
    Name = "iicpc-compiler"
  }
}

resource "aws_ecr_repository" "testing" {
  name                 = "iicpc-testing"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = {
    Name = "iicpc-testing"
  }
}

# Single ECR Repository for all contestant builds (tagged by submission_id)
resource "aws_ecr_repository" "contestants" {
  name                 = "iicpc-contestants"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = false # Speed up compile cycles
  }

  tags = {
    Name = "iicpc-contestants"
  }
}

resource "aws_ecr_repository" "init_db" {
  name                 = "iicpc-init-db"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = {
    Name = "iicpc-init-db"
  }
}

