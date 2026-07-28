# PulseTrace Helm chart

Packages the PulseTrace **application** microservices (gateway, log, alert,
correlation, topology, action, notification) as a single release. The raw
manifests in `k8s/` remain the reference; this chart is the parameterized,
multi-environment way to ship them.

> **Data stores are not included.** PostgreSQL, ClickHouse, Neo4j, Kafka, Redis,
> and RabbitMQ are expected to be provided externally — managed services (SaaS)
> or the customer's own operators (enterprise) — and wired in via `.Values.config`
> and the referenced Secret. This matches the `k8s/` manifests, which also assume
> external stores.

## Install

```bash
# 1. Create the credential Secret first (the chart never creates it):
kubectl create namespace pulsetrace
kubectl apply -n pulsetrace -f k8s/secret.yaml        # from secret.yaml.example
#    …or sync it with the External Secrets Operator (k8s/externalsecret.yaml.example)

# 2a. SaaS / shared-cluster defaults (autoscaling on, local/registry images):
helm install pulsetrace ./helm/pulsetrace -n pulsetrace

# 2b. Enterprise on-prem (pinned images, fixed replicas, TLS ingress):
helm install pulsetrace ./helm/pulsetrace -n pulsetrace \
  -f helm/pulsetrace/values-enterprise.yaml
```

Render without installing to review the output:

```bash
helm template pulsetrace ./helm/pulsetrace -n pulsetrace
```

## What it renders

| Resource | Notes |
| --- | --- |
| ConfigMap `pulsetrace-config` | from `.Values.config`, injected into every pod via `envFrom` |
| Deployment + Service (×N) | one per service in `.Values.services`; a service with no `port` (notification-service) gets a Deployment only |
| HorizontalPodAutoscaler | for services with `autoscaling.enabled` (gateway, log, notification by default) |
| PodDisruptionBudget | for services with `pdb.enabled` (the multi-replica ones) |
| ServiceAccount + Role + RoleBinding | action-service's namespace-scoped permission to remediate Deployments |
| Ingress | routes `/` to the gateway |

## Key values

| Path | Purpose |
| --- | --- |
| `image.registry` / `image.tag` | image source; empty registry + `latest` matches local dev, set both for a real registry |
| `existingSecret` | name of the pre-created credential Secret (default `pulsetrace-secrets`) — the chart references, never creates it |
| `config.*` | non-secret env, rendered into the ConfigMap |
| `services.<name>.autoscaling` | HPA min/max/target; when enabled, the Deployment omits a static replica count so the HPA owns it |
| `services.<name>.pdb` | PodDisruptionBudget; only enable for services running ≥ 2 replicas |
| `ingress.*` | host, class, annotations, TLS |

Autoscaling requires `metrics-server` in the cluster; the enterprise overlay
turns it off in favor of fixed, capacity-planned replica counts.
