# PulseTrace Kubernetes manifests

Raw manifests for a full PulseTrace deployment. Apply `namespace.yaml` first,
then the secret (see below), then `configmap.yaml` and the per-service manifests.

> Prefer a parameterized, multi-environment install? Use the Helm chart in
> [`helm/pulsetrace`](../helm/pulsetrace/README.md) — it renders these same
> resources with SaaS and enterprise value sets. These raw manifests remain the
> reference.

## Secrets

`k8s/secret.yaml` is **git-ignored** — it must never be committed, because real
deployments edit it in place and a committed real value is a leak that lives
forever in git history. Two supported ways to provide secrets:

### Local / single-tenant dev

```bash
cp k8s/secret.yaml.example k8s/secret.yaml
# edit k8s/secret.yaml, fill in real values
kubectl apply -f k8s/secret.yaml
```

### Production / shared / multi-tenant

Do **not** hand-manage the Secret. Sync it from a real secret backend with the
[External Secrets Operator](https://external-secrets.io):

```bash
# 1. install ESO (once per cluster)
# 2. put the values in your backend (Vault / AWS / GCP / Azure secret manager)
# 3. apply the ExternalSecret — it materializes `pulsetrace-secrets` for you
kubectl apply -f k8s/externalsecret.yaml.example   # after adapting the SecretStore
```

`externalsecret.yaml.example` produces the same `pulsetrace-secrets` Secret every
Deployment already references, so no other manifest changes. It works for both
GTM models: point the `SecretStore` at your cloud secret manager (SaaS) or at the
customer's in-cluster Vault (enterprise on-prem).

## Availability & autoscaling

- **HorizontalPodAutoscalers**: `gateway` (2→8 on CPU) and `log-service` (2→10 on
  CPU+memory) — the two ingestion-path services — scale with load. Requires
  `metrics-server` in the cluster.
- **PodDisruptionBudgets** (`poddisruptionbudgets.yaml`): `minAvailable: 1` for
  the multi-replica stateless services (gateway, log, alert, notification), so a
  node drain / cluster upgrade can never evict every replica of a service at
  once. `correlation`, `topology`, and `action` run a single replica by design
  (they host background singletons that must not be double-scheduled), so they
  intentionally have no PDB — making them HA needs leader election, not a PDB.

## Key security-relevant settings

| Key | Why it matters |
| --- | --- |
| `JWT_SECRET` | Required. If unset, gateway-service uses a random per-process secret and every session dies on restart. |
| `REQUIRE_INGESTION_KEY` | Set `"true"` for any multi-tenant deployment. When true, telemetry ingestion must present a valid per-tenant ingestion key (`Authorization: Bearer <key>`), so tenant identity can't be spoofed via a header. Enforced identically on the HTTP paths **and** the in-process OTLP/gRPC receiver (:4317). Mint keys with `POST /api/v1/admin/ingestion-keys`. |
| `OTLP_TLS_CERT_FILE` / `OTLP_TLS_KEY_FILE` | Serve the OTLP/gRPC receiver (:4317) over TLS so the ingestion key isn't sent in cleartext. Mount the cert/key from a Secret and point these at the files. Omit both only when a TLS-terminating LB/ingress already fronts :4317. Setting just one fails startup. |
| `OTLP_TLS_CLIENT_CA_FILE` | Optional. Requires mTLS on :4317 — clients must present a cert signed by this CA, a transport-level auth factor on top of the ingestion key. |

### Keep the OTel Collector internal

The gateway is the only authenticated, tenant-attributing ingestion front door —
including for the Datadog/Splunk "Trojan Horse" migration paths, which it
terminates and authenticates itself (see the README's "Zero-code migration"
section). The **OTel Collector must never be exposed** (no Service of type
LoadBalancer/NodePort, no Ingress on its OTLP/Datadog/Splunk ports): its native
receivers do no per-tenant auth or tenant stamping, so a reachable collector is
an un-tenanted ingestion bypass. Restrict it with a NetworkPolicy that only
admits the gateway and internal self-instrumentation, e.g.:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: otel-collector-ingress
  namespace: pulsetrace
spec:
  podSelector:
    matchLabels: { app: otel-collector }
  policyTypes: ["Ingress"]
  ingress:
    - from:
        - podSelector: {}   # only pods in this namespace (gateway + services)
```
| `PLAYBOOK_HMAC_SECRET` | Must be identical on correlation-service and topology-service. |
