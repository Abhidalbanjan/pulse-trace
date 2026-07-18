Week 4: Zero-Egress Observability & Enterprise Sharding in Go!

Streaming terabytes of raw logs and traces to a SaaS APM is a major security risk—and a massive cloud network egress bill.

This week on PulseTrace, I focused on solving this enterprise bottleneck by building a privacy-first, zero-egress architecture. Here is what went live:

Zero-Data-Egress Hybrid Architecture
- The Design: Raw telemetry and ClickHouse databases stay secure inside the customer’s cloud network. The central SaaS control plane only receives anonymized metadata and status graphs.
- The Privacy: All service names are hashed (svc_xxxx) and incident logs are scrubbed before forwarding to secure system topologies.
- The Savings: Bandwidth is reduced by 99.96% (compressing a 1.4MB raw telemetry surge down to just 518 bytes of incident metadata—a 2,702x network egress reduction).

Dynamic ClickHouse Sharding
- Multi-tenancy isolation implemented at the database tier.
- Ingestion routing uses standard and enterprise database connection pools. While standard tenants share a database pool, enterprise clients are dynamically routed to their own dedicated physical ClickHouse database shards.

Real-Time PII Sanitizer Middleware
- High-performance regular expression parsing middleware running directly inside the API gateway.
- Intercepts telemetry ingestion routes and masks Credit Cards, Emails, SSNs, and Client Secrets/Passwords before they are queued to Kafka or written to disk.

Code: [GitHub Repository Link]

#golang #observability #opentelemetry #clickhouse #cybersecurity #devops #sre #softwareengineering
