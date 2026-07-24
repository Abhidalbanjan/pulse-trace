# PulseTrace Kubernetes manifests

Raw manifests for a full PulseTrace deployment. Apply `namespace.yaml` first,
then the secret (see below), then `configmap.yaml` and the per-service manifests.

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

## Key security-relevant settings

| Key | Why it matters |
| --- | --- |
| `JWT_SECRET` | Required. If unset, gateway-service uses a random per-process secret and every session dies on restart. |
| `REQUIRE_INGESTION_KEY` | Set `"true"` for any multi-tenant deployment. When true, telemetry ingestion must present a valid per-tenant ingestion key (`Authorization: Bearer <key>`), so tenant identity can't be spoofed via a header. Mint keys with `POST /api/v1/admin/ingestion-keys`. |
| `PLAYBOOK_HMAC_SECRET` | Must be identical on correlation-service and topology-service. |
