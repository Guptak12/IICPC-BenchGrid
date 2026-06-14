#!/usr/bin/env bash
# deploy_monitoring.sh — Deploy kube-prometheus-stack (Prometheus + Grafana) into EKS
# Run this ONCE after the core platform is up (after deploy_aws.sh).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

AWS_REGION="us-east-1"
GRAFANA_PASSWORD="${GRAFANA_ADMIN_PASSWORD:-iicpc-admin-2026}"

echo "=== 1. Adding Helm repos ==="
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

echo "=== 2. Creating monitoring namespace ==="
kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -

echo "=== 3. Installing kube-prometheus-stack ==="
# Storage: Prometheus uses a 20Gi EBS gp2 volume for metrics persistence.
# Grafana: exposed via ALB Ingress on /grafana path (same ALB as gateway).
# Scrape: auto-discovers all pods with prometheus.io/scrape=true annotations.
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --version 61.3.2 \
  --timeout 10m \
  --set grafana.adminPassword="${GRAFANA_PASSWORD}" \
  --set grafana.grafana.ini.server.root_url="http://%(domain)s/grafana" \
  --set grafana.grafana.ini.server.serve_from_sub_path=true \
  --set prometheus.prometheusSpec.storageSpec.volumeClaimTemplate.spec.storageClassName=gp2 \
  --set prometheus.prometheusSpec.storageSpec.volumeClaimTemplate.spec.resources.requests.storage=20Gi \
  --set prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues=false \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --set prometheus.prometheusSpec.retention=7d \
  --set alertmanager.enabled=false \
  --set nodeExporter.enabled=true \
  --set kubeStateMetrics.enabled=true \
  2>&1

echo "=== 4. Creating Grafana ALB Ingress ==="
cat <<EOF | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: grafana-ingress
  namespace: monitoring
  annotations:
    kubernetes.io/ingress.class: alb
    alb.ingress.kubernetes.io/scheme: internet-facing
    alb.ingress.kubernetes.io/target-type: ip
    alb.ingress.kubernetes.io/listen-ports: '[{"HTTP": 80}]'
    alb.ingress.kubernetes.io/group.name: iicpc-benchgrid
    alb.ingress.kubernetes.io/group.order: "20"
spec:
  ingressClassName: alb
  rules:
  - http:
      paths:
      - path: /grafana
        pathType: Prefix
        backend:
          service:
            name: kube-prometheus-stack-grafana
            port:
              number: 80
EOF

echo "=== 5. Applying platform ServiceMonitors (scrape iicpc services) ==="
cat <<EOF | kubectl apply -f -
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: iicpc-gateway
  namespace: monitoring
  labels:
    release: kube-prometheus-stack
spec:
  namespaceSelector:
    matchNames: [default]
  selector:
    matchLabels:
      app: submission-gateway
  endpoints:
  - port: metrics
    path: /metrics
    interval: 15s
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: iicpc-compiler
  namespace: monitoring
  labels:
    release: kube-prometheus-stack
spec:
  namespaceSelector:
    matchNames: [default]
  selector:
    matchLabels:
      app: compilation-worker
  endpoints:
  - port: metrics
    path: /metrics
    interval: 15s
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: iicpc-testing
  namespace: monitoring
  labels:
    release: kube-prometheus-stack
spec:
  namespaceSelector:
    matchNames: [default]
  selector:
    matchLabels:
      app: testing-worker
  endpoints:
  - port: metrics
    path: /metrics
    interval: 15s
EOF

echo "=== 6. Waiting for Grafana to be ready ==="
kubectl rollout status deployment/kube-prometheus-stack-grafana -n monitoring --timeout=180s

echo ""
echo "=== Monitoring Stack Deployed! ==="
GRAFANA_ALB=$(kubectl get ingress grafana-ingress -n monitoring \
  -o jsonpath='{.status.loadBalancer.ingress[0].hostname}' 2>/dev/null || echo "(provisioning...)")
echo "  Grafana URL:    http://${GRAFANA_ALB}/grafana"
echo "  Admin user:     admin"
echo "  Admin password: ${GRAFANA_PASSWORD}"
echo ""
echo "  Tip: set GRAFANA_ADMIN_PASSWORD env var before running to use a custom password"
