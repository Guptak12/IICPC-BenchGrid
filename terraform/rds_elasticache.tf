# Security Group for EKS Compute nodes (used for DB/Cache access rules)
resource "aws_security_group" "eks_nodes_sg" {
  name        = "${var.cluster_name}-nodes-sg"
  description = "Security group for EKS compute nodes to access databases and services"
  vpc_id      = aws_vpc.main.id

  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }

  tags = {
    Name = "${var.cluster_name}-nodes-sg"
  }
}

# PostgreSQL Database security group
resource "aws_security_group" "db_sg" {
  name        = "${var.cluster_name}-db-sg"
  description = "Allow inbound PostgreSQL traffic from EKS nodes"
  vpc_id      = aws_vpc.main.id

  ingress {
    description     = "Postgres access from Terraform-managed EKS nodes SG"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.eks_nodes_sg.id]
  }

  # EKS node groups auto-create their own managed security group (separate from our custom SG).
  # We must also allow that SG or pods can't reach RDS.
  ingress {
    description     = "Postgres access from EKS-managed node group SG"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_eks_node_group.core.resources[0].remote_access_security_group_id == null ? "sg-034b63cd0710aac98" : aws_eks_node_group.core.resources[0].remote_access_security_group_id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${var.cluster_name}-db-sg"
  }
}

# ElastiCache Redis security group
resource "aws_security_group" "cache_sg" {
  name        = "${var.cluster_name}-cache-sg"
  description = "Allow inbound Redis traffic from EKS nodes"
  vpc_id      = aws_vpc.main.id

  ingress {
    description     = "Redis access from EKS nodes"
    from_port       = 6379
    to_port         = 6379
    protocol        = "tcp"
    security_groups = [aws_security_group.eks_nodes_sg.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${var.cluster_name}-cache-sg"
  }
}

# Subnet Groups
resource "aws_db_subnet_group" "db" {
  name       = "${var.cluster_name}-db-subnet-group"
  subnet_ids = aws_subnet.database[*].id

  tags = {
    Name = "${var.cluster_name}-db-subnet-group"
  }
}

resource "aws_elasticache_subnet_group" "cache" {
  name       = "${var.cluster_name}-cache-subnet-group"
  subnet_ids = aws_subnet.cache[*].id

  tags = {
    Name = "${var.cluster_name}-cache-subnet-group"
  }
}

# RDS PostgreSQL Instance
resource "aws_db_instance" "postgres" {
  identifier             = "${var.cluster_name}-db"
  allocated_storage      = 20
  max_allocated_storage  = 100
  engine                 = "postgres"
  engine_version         = "16.4"
  instance_class         = "db.t3.micro"
  db_name                = "iicpc_db"
  username               = var.db_username
  password               = var.db_password
  db_subnet_group_name   = aws_db_subnet_group.db.name
  vpc_security_group_ids = [aws_security_group.db_sg.id]
  skip_final_snapshot    = true

  tags = {
    Name = "${var.cluster_name}-db"
  }
}

# ElastiCache Redis Replication Group (Demo scale)
resource "aws_elasticache_replication_group" "redis" {
  replication_group_id        = "${var.cluster_name}-cache"
  description                 = "Redis cache and queue stream for BenchGrid"
  node_type                   = var.redis_node_type
  num_cache_clusters          = 1
  parameter_group_name        = "default.redis7"
  port                        = 6379
  subnet_group_name           = aws_elasticache_subnet_group.cache.name
  security_group_ids          = [aws_security_group.cache_sg.id]
  automatic_failover_enabled  = false

  tags = {
    Name = "${var.cluster_name}-cache"
  }
}
