-- Shift-left deploy gates (ROAD_TO_100 · F5).
--
-- Each row is one PR the GitHub webhook ran through PulseTrace's SLO-risk
-- evaluator, recorded so the Deploy Gates screen can show the decision history
-- (the gate itself is otherwise stateless: PR in → AI verdict → GitHub status).
--
-- Tenant scoping: the GitHub webhook is unauthenticated (GitHub can't present a
-- JWT), so rows land in the tenant that owns the deployment. Today that is
-- 'default' — correct for single-tenant (enterprise / on-prem) installs. A
-- SaaS repo→tenant mapping is a documented follow-up (see F5 in ROAD_TO_100).
CREATE TABLE IF NOT EXISTS deploy_gates (
    id         VARCHAR(64) PRIMARY KEY,
    tenant_id  VARCHAR(50) NOT NULL DEFAULT 'default',
    pr_number  INTEGER NOT NULL,
    title      TEXT NOT NULL,
    author     VARCHAR(255),
    repo       VARCHAR(255),
    sha        VARCHAR(64),
    decision   VARCHAR(16) NOT NULL, -- APPROVE | BLOCK
    reason     TEXT,
    pr_url     TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
);

-- Backs the screen's "recent gates for this tenant, newest first" query.
CREATE INDEX IF NOT EXISTS idx_deploy_gates_tenant_created ON deploy_gates (tenant_id, created_at DESC);
