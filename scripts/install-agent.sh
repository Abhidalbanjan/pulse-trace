#!/bin/bash
# PulseTrace 1-Line Bootstrap Script
# Usage: curl -sSL https://pulsetrace.io/install-agent.sh | bash

set -e

echo "=========================================================="
echo "⚡️ Welcome to PulseTrace 1-Line Bootstrap ⚡️"
echo "=========================================================="
echo ""

echo "[1/4] Detecting OS and Architecture..."
OS=$(uname -s | tr A-Z a-z)
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then ARCH="amd64"; fi
if [ "$ARCH" = "aarch64" ]; then ARCH="arm64"; fi

echo "[2/4] Downloading OpenTelemetry Collector for $OS/$ARCH..."
OTEL_VERSION="0.101.0"
DOWNLOAD_URL="https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/v${OTEL_VERSION}/otelcol-contrib_${OTEL_VERSION}_${OS}_${ARCH}.tar.gz"

if ! curl -sSL -o otelcol.tar.gz "$DOWNLOAD_URL"; then
  echo "❌ Failed to download the collector. Please check your OS/Arch compatibility."
  exit 1
fi

tar -xzf otelcol.tar.gz otelcol-contrib
rm otelcol.tar.gz

echo "[3/4] Configuring Trojan Horse endpoints..."
cat << 'EOF' > config.yaml
receivers:
  datadog:
    endpoint: 0.0.0.0:8126
  splunk_hec:
    endpoint: 0.0.0.0:8088
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

exporters:
  otlp:
    endpoint: "gateway.pulsetrace.io:4317" # Replace with actual Gateway URL
    tls:
      insecure: false

service:
  pipelines:
    traces:
      receivers: [datadog, splunk_hec, otlp]
      exporters: [otlp]
    metrics:
      receivers: [datadog, splunk_hec, otlp]
      exporters: [otlp]
    logs:
      receivers: [datadog, splunk_hec, otlp]
      exporters: [otlp]
EOF

echo "[4/4] Starting Agent in background..."
chmod +x otelcol-contrib
./otelcol-contrib --config=config.yaml > otelcol.log 2>&1 &

echo ""
echo "✅ PulseTrace Agent successfully installed and running!"
echo "📡 Listening for Datadog payloads on port 8126"
echo "📡 Listening for Splunk payloads on port 8088"
echo "📡 Listening for OTLP payloads on ports 4317/4318"
echo "=========================================================="
