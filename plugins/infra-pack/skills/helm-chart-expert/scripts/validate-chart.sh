#!/bin/bash
# Validate Helm charts - lint + template dry-run
# Usage: ./validate-chart.sh <chart-path> [values-file]

set -euo pipefail

CHART_PATH="${1:?Usage: validate-chart.sh <chart-path> [values-file]}"
VALUES_FILE="${2:-}"

echo "=== Helm Chart Validation ==="
echo "Chart: $CHART_PATH"
[ -n "$VALUES_FILE" ] && echo "Values: $VALUES_FILE"
echo ""

# Step 1: Lint
echo "--- Step 1: Helm Lint ---"
if [ -n "$VALUES_FILE" ]; then
  helm lint "$CHART_PATH" -f "$VALUES_FILE"
else
  helm lint "$CHART_PATH"
fi
echo ""

# Step 2: Template rendering
echo "--- Step 2: Template Render ---"
if [ -n "$VALUES_FILE" ]; then
  helm template test-release "$CHART_PATH" -f "$VALUES_FILE" --set ingress.enabled=true > /dev/null
else
  helm template test-release "$CHART_PATH" > /dev/null
fi
echo "Template renders successfully"
echo ""

# Step 3: Check Chart.yaml version
echo "--- Step 3: Version Check ---"
VERSION=$(grep '^version:' "$CHART_PATH/Chart.yaml" | awk '{print $2}')
echo "Chart version: $VERSION"

# Step 4: Check for common issues
echo ""
echo "--- Step 4: Common Issues Check ---"

# Check for hardcoded values in templates
if grep -r 'image:.*:latest' "$CHART_PATH/templates/" 2>/dev/null; then
  echo "WARNING: Found :latest image tag in templates"
fi

# Check for missing resource limits
if ! grep -r 'resources:' "$CHART_PATH/templates/" 2>/dev/null | grep -q 'resources:'; then
  echo "WARNING: No resource limits found in templates"
fi

echo ""
echo "=== Validation Complete ==="
