Week 5: Auto-Topology Discovery & AI Self-Healing in Go!

Reacting to production alerts after the damage is done is a massive SRE bottleneck. 

This week on PulseTrace, I shifted the platform from reactive metrics to proactive, automated self-healing. Here is what went live:

- Auto-Topology Discovery: Automatically builds live service dependency graphs in Neo4j by parsing incoming OpenTelemetry traces (parent-child span relationships) in real-time.
- High-Performance Caching: Integrated Redis to cache topology queries, reducing graph lookup latency during critical Root Cause Analysis (RCA) loops.
- Real-World Self-Healing: The Causal AI triggers signed recovery playbooks when confidence is >= 70%. The agent executes actual kubectl rollout restarts, replica scaling, or database connection terminations (pg_terminate_backend).
- Cryptographic Safety: Enforces HMAC-SHA256 signature verification and a strict 5-minute window check to prevent replay attacks on agent execution.

Code: [GitHub Repository Link]

#golang #observability #opentelemetry #redis #automation #sre #devops #softwareengineering
