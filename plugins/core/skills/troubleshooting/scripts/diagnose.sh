#!/bin/bash
# Platform Diagnostic Script
# Usage: ./diagnose.sh [namespace]
# Environment overrides: DEFAULT_NAMESPACE, INGRESS_DOMAIN, FLEET_SIZE (see SKILL.md)

NAMESPACE="${1:-${DEFAULT_NAMESPACE:-default}}"
INGRESS_DOMAIN="${INGRESS_DOMAIN:-d16.local}"
FLEET_SIZE="${FLEET_SIZE:-3}"

echo "=== Platform Diagnostics ==="
echo "Namespace: $NAMESPACE"
echo "Date: $(date)"
echo ""

# 1. Pod Status
echo "--- 1. Pod Status ---"
kubectl get pods -n "$NAMESPACE" -o wide 2>/dev/null || echo "Cannot reach cluster"
echo ""

# 2. Recent Events
echo "--- 2. Recent Events (last 10) ---"
kubectl get events -n "$NAMESPACE" --sort-by='.lastTimestamp' 2>/dev/null | tail -10
echo ""

# 3. Ingress
echo "--- 3. Ingress ---"
kubectl get ingress -n "$NAMESPACE" 2>/dev/null
echo ""

# 4. Services
echo "--- 4. Services ---"
kubectl get svc -n "$NAMESPACE" 2>/dev/null
echo ""

# 5. Health Checks
echo "--- 5. Health Endpoints ---"
for domain in "$INGRESS_DOMAIN" "api.$INGRESS_DOMAIN"; do
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://$domain/health" 2>/dev/null || echo "unreachable")
  echo "$domain/health -> $STATUS"
done

# Check per-device health endpoints (fleet size: see FLEET_SIZE in SKILL.md)
for i in $(seq 1 "$FLEET_SIZE"); do
  STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://d${i}.${INGRESS_DOMAIN}/health" 2>/dev/null || echo "unreachable")
  echo "d${i}.${INGRESS_DOMAIN}/health -> $STATUS"
done
echo ""

# 6. Resource Usage
echo "--- 6. Resource Usage ---"
kubectl top pods -n "$NAMESPACE" 2>/dev/null || echo "Metrics server not available"
echo ""

echo "=== Diagnostics Complete ==="
