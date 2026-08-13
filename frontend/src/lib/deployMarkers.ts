// Reusable deploy-marker snapping (Deploy Gates · E1).
//
// A time-series chart's x-axis is a categorical set of bucket labels; a
// deployment happened at an arbitrary instant. To overlay it we snap the deploy
// to the nearest bucket whose label the chart already renders. Kept as a pure
// module so any chart view (Metrics, Services, SLO, Errors) can reuse the exact
// same logic instead of re-implementing it.

export interface DeployInput {
  deployed_at: string;
  version: string;
}

export interface ChartBucket {
  label: string; // the x-axis category the chart renders
  ms: number; // that bucket's instant in epoch-ms
}

export interface DeployMarker {
  version: string;
  label: string; // the bucket label to anchor a ReferenceLine at
}

// toEpochMs parses the timestamp strings the backend emits — ClickHouse
// (`2026-08-13 10:00:00`, no tz → treated as UTC) and Postgres::text
// (`2026-08-13 10:00:00+00`) — into epoch-ms, consistently in UTC so chart
// buckets and deploy times can't drift by a timezone.
export function toEpochMs(s: string): number {
  if (!s) return NaN;
  let x = s.trim().replace(' ', 'T').replace(/(\.\d{3})\d+/, '$1'); // ns → ms
  if (/[+-]\d{2}$/.test(x)) {
    x += ':00'; // Postgres "+00" → "+00:00"
  } else if (!/[zZ]|[+-]\d{2}:?\d{2}$/.test(x)) {
    x += 'Z'; // no offset → UTC
  }
  const p = Date.parse(x);
  return Number.isNaN(p) ? NaN : p;
}

// snapDeploymentsToBuckets maps each deployment to the nearest chart bucket.
// Deployments and buckets with unparseable timestamps are skipped rather than
// mis-anchored. Order of output follows the input deployments.
export function snapDeploymentsToBuckets(deployments: DeployInput[], buckets: ChartBucket[]): DeployMarker[] {
  const valid = buckets.filter((b) => Number.isFinite(b.ms));
  if (valid.length === 0) return [];

  const out: DeployMarker[] = [];
  for (const d of deployments) {
    const dms = toEpochMs(d.deployed_at);
    if (!Number.isFinite(dms)) continue;
    let closest = valid[0];
    let best = Math.abs(valid[0].ms - dms);
    for (const b of valid) {
      const delta = Math.abs(b.ms - dms);
      if (delta < best) {
        best = delta;
        closest = b;
      }
    }
    out.push({ version: d.version, label: closest.label });
  }
  return out;
}
