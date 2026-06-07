#!/bin/bash
# demo_incident.sh
# Ingests a series of cascading logs to trigger a causal chain

echo "🚀 Simulating Incident Cascade..."

# 1. Base issue: Postgres pool exhaustion
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "postgres", "level": "ERROR", "message": "connection pool exhausted: active connections 100/100"}'

sleep 1

# 2. Dependency failure: payment-service times out on database connection
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "payment-service", "level": "ERROR", "message": "database query timeout after 5000ms"}'

sleep 1

# 3. Upstream failure: order-service reports degradation because payment-service is slow
curl -X POST http://localhost:8080/api/v1/logs \
  -H "Content-Type: application/json" \
  -d '{"service": "order-service", "level": "ERROR", "message": "payment check failed: upstream service unavailable"}'

echo -e "\n✅ Cascade injected! Check incidents on http://localhost:8080/api/v1/incidents"
