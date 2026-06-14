# Enable OIDC Provider for EKS Cluster IRSA integration
data "tls_certificate" "eks" {
  url = aws_eks_cluster.main.identity[0].oidc[0].issuer
}

resource "aws_iam_openid_connect_provider" "eks" {
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.eks.certificates[0].sha1_fingerprint]
  url             = aws_eks_cluster.main.identity[0].oidc[0].issuer
}

# IAM Role for EBS CSI Driver (required for PersistentVolume provisioning on K8s 1.23+)
resource "aws_iam_role" "ebs_csi_driver" {
  name = "AmazonEKS_EBS_CSI_DriverRole"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Federated = aws_iam_openid_connect_provider.eks.arn
      }
      Action = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${replace(aws_eks_cluster.main.identity[0].oidc[0].issuer, "https://", "")}:aud" = "sts.amazonaws.com"
          "${replace(aws_eks_cluster.main.identity[0].oidc[0].issuer, "https://", "")}:sub" = "system:serviceaccount:kube-system:ebs-csi-controller-sa"
        }
      }
    }]
  })

  lifecycle {
    # Role already created manually; allow Terraform to adopt it without error
    ignore_changes = [assume_role_policy]
  }
}

resource "aws_iam_role_policy_attachment" "ebs_csi_driver" {
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
  role       = aws_iam_role.ebs_csi_driver.name
}


# IAM Policy for S3 Access (Gateway + Compiler + Testing pods)
resource "aws_iam_policy" "s3_access" {
  name        = "${var.cluster_name}-s3-access-policy"
  description = "Allows read/write permissions to the BenchGrid submissions S3 bucket"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "s3:GetObject",
          "s3:PutObject",
          "s3:DeleteObject",
          "s3:ListBucket"
        ]
        Resource = [
          aws_s3_bucket.submissions.arn,
          "${aws_s3_bucket.submissions.arn}/*"
        ]
      }
    ]
  })
}

# IAM Policy for ECR Pushing (Compiler / Kaniko pods)
resource "aws_iam_policy" "ecr_push_access" {
  name        = "${var.cluster_name}-ecr-push-policy"
  description = "Allows pushing contestant images to the contestants ECR registry"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "ecr:GetAuthorizationToken",
          "ecr:BatchCheckLayerAvailability",
          "ecr:GetDownloadUrlForLayer",
          "ecr:GetRepositoryPolicy",
          "ecr:DescribeRepositories",
          "ecr:ListImages",
          "ecr:DescribeImages",
          "ecr:BatchGetImage",
          "ecr:InitiateLayerUpload",
          "ecr:UploadLayerPart",
          "ecr:CompleteLayerUpload",
          "ecr:PutImage"
        ]
        Resource = "*" # ECR authentication requires * resource scope
      }
    ]
  })
}

# IRSA Role for the Gateway service (Default Namespace)
resource "aws_iam_role" "gateway_pod_role" {
  name = "${var.cluster_name}-gateway-pod-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Federated = aws_iam_openid_connect_provider.eks.arn
        }
        Action = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "${replace(aws_eks_cluster.main.identity[0].oidc[0].issuer, "https://", "")}:sub" = "system:serviceaccount:default:iicpc-gateway"
          }
        }
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "gateway_s3" {
  policy_arn = aws_iam_policy.s3_access.arn
  role       = aws_iam_role.gateway_pod_role.name
}

# IRSA Role for the Compiler worker (Default Namespace) - allows Kaniko job spawning
resource "aws_iam_role" "compiler_pod_role" {
  name = "${var.cluster_name}-compiler-pod-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Federated = aws_iam_openid_connect_provider.eks.arn
        }
        Action = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "${replace(aws_eks_cluster.main.identity[0].oidc[0].issuer, "https://", "")}:sub" = [
              "system:serviceaccount:default:iicpc-compiler",
              "system:serviceaccount:default:kaniko-sa" # Also map to the ServiceAccount Kaniko runs under
            ]
          }
        }
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "compiler_s3" {
  policy_arn = aws_iam_policy.s3_access.arn
  role       = aws_iam_role.compiler_pod_role.name
}

resource "aws_iam_role_policy_attachment" "compiler_ecr" {
  policy_arn = aws_iam_policy.ecr_push_access.arn
  role       = aws_iam_role.compiler_pod_role.name
}

# IRSA Role for the Testing worker (Default Namespace)
resource "aws_iam_role" "testing_pod_role" {
  name = "${var.cluster_name}-testing-pod-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Federated = aws_iam_openid_connect_provider.eks.arn
        }
        Action = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "${replace(aws_eks_cluster.main.identity[0].oidc[0].issuer, "https://", "")}:sub" = "system:serviceaccount:default:iicpc-testing"
          }
        }
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "testing_s3" {
  policy_arn = aws_iam_policy.s3_access.arn
  role       = aws_iam_role.testing_pod_role.name
}

# Output variables to use in EKS ServiceAccount annotations
output "gateway_role_arn" {
  value = aws_iam_role.gateway_pod_role.arn
}

output "compiler_role_arn" {
  value = aws_iam_role.compiler_pod_role.arn
}

output "testing_role_arn" {
  value = aws_iam_role.testing_pod_role.arn
}

output "s3_bucket_name" {
  value = aws_s3_bucket.submissions.id
}

output "rds_endpoint" {
  value = aws_db_instance.postgres.endpoint
}

output "redis_endpoint" {
  value = aws_elasticache_replication_group.redis.primary_endpoint_address
}

# IAM policy for AWS Load Balancer Controller
resource "aws_iam_policy" "aws_load_balancer_controller" {
  name        = "${var.cluster_name}-alb-controller-policy"
  description = "Permissions required by EKS AWS Load Balancer Controller"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "iam:CreateServiceLinkedRole",
          "ec2:DescribeAccountAttributes",
          "ec2:DescribeAddresses",
          "ec2:DescribeAvailabilityZones",
          "ec2:DescribeInternetGateways",
          "ec2:DescribeVpcs",
          "ec2:DescribeSubnets",
          "ec2:DescribeSecurityGroups",
          "ec2:DescribeInstances",
          "ec2:DescribeNetworkInterfaces",
          "ec2:DescribeTags",
          "ec2:GetCoipPoolDetails",
          "ec2:GetResourcePolicy",
          "elasticloadbalancing:*",
          "cognito-idp:DescribeUserPoolClient",
          "acm:ListCertificates",
          "acm:DescribeCertificate",
          "iam:ListServerCertificates",
          "iam:GetServerCertificate",
          "waf-regional:*",
          "wafv2:*",
          "shield:*",
          "servicecatalog:ListAcceptedPortfolioShares",
          "sts:AssumeRole"
        ]
        Resource = "*"
      }
    ]
  })
}

resource "aws_iam_role" "aws_load_balancer_controller" {
  name = "${var.cluster_name}-alb-controller-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Principal = {
          Federated = aws_iam_openid_connect_provider.eks.arn
        }
        Action = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "${replace(aws_eks_cluster.main.identity[0].oidc[0].issuer, "https://", "")}:sub" = "system:serviceaccount:kube-system:aws-load-balancer-controller"
          }
        }
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "aws_load_balancer_controller" {
  policy_arn = aws_iam_policy.aws_load_balancer_controller.arn
  role       = aws_iam_role.aws_load_balancer_controller.name
}

output "alb_controller_role_arn" {
  value = aws_iam_role.aws_load_balancer_controller.arn
}

output "ecr_registry_url" {
  value = split("/", aws_ecr_repository.gateway.repository_url)[0]
}

output "aws_region" {
  value = var.aws_region
}



