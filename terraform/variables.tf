variable "aws_region" {
  description = "AWS region to deploy resources"
  type        = string
  default     = "us-east-1"
}

variable "cluster_name" {
  description = "Name of the EKS cluster"
  type        = string
  default     = "iicpc-benchgrid"
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

variable "public_subnets" {
  description = "Public subnets for Gateway and load balancers"
  type        = list(string)
  default     = ["10.0.1.0/24", "10.0.2.0/24"]
}

variable "private_subnets" {
  description = "Private subnets for core EKS cluster node groups"
  type        = list(string)
  default     = ["10.0.10.0/24", "10.0.11.0/24"]
}

variable "database_subnets" {
  description = "Private subnets for PostgreSQL RDS"
  type        = list(string)
  default     = ["10.0.20.0/24", "10.0.21.0/24"]
}

variable "cache_subnets" {
  description = "Private subnets for ElastiCache Redis"
  type        = list(string)
  default     = ["10.0.30.0/24", "10.0.31.0/24"]
}

variable "db_username" {
  description = "Database admin username"
  type        = string
  default     = "iicpc"
}

variable "db_password" {
  description = "Database admin password"
  type        = string
  default     = "iicpc_secret_production"
  sensitive   = true
}

variable "redis_node_type" {
  description = "Node type for ElastiCache Redis"
  type        = string
  default     = "cache.t3.micro"
}

variable "core_node_instance_type" {
  description = "Instance type for EKS core node group"
  type        = string
  default     = "t3.medium"
}

variable "sandbox_node_instance_type" {
  description = "Instance type for EKS sandbox node group"
  type        = string
  default     = "c6i.large"
}
