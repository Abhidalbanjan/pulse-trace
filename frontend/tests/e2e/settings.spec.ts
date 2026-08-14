import { test, expect } from '@playwright/test';

test.describe('Settings', () => {
  test('Users tab lists seeded users', async ({ page }) => {
    await page.goto('/settings');
    await expect(page.getByText('User Management')).toBeVisible();
    // Scope to the content panel: the sidebar footer always shows the logged-in
    // user's own name, which can collide with a seeded username/role string.
    await expect(page.locator('main').getByText('sarah.oncall').first()).toBeVisible({ timeout: 10000 });
  });

  test('Roles tab lists seeded roles with permissions', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Roles (RBAC)' }).click();
    // Scope to the roles table row, not page-wide: the sidebar footer also
    // renders the current user's role ("admin"), which would otherwise collide.
    await expect(page.locator('table tbody tr', { hasText: 'admin' }).first()).toBeVisible({ timeout: 10000 });
    // Exact cell match: the seeded role's own description begins "Support
    // engineers triaging…", so a substring match hits both the name and the
    // description cell.
    await expect(
      page.getByRole('cell', { name: 'support', exact: true }).first(),
    ).toBeVisible();
  });

  test('Policies tab lists seeded ABAC policies', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Policies (ABAC)' }).click();
    await expect(page.getByText('viewer-write-block')).toBeVisible({ timeout: 10000 });
  });

  test('guided policy builder composes and validates a condition (F18)', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Policies (ABAC)' }).click();
    await page.getByRole('button', { name: '+ New Policy' }).click();

    // Fill a guided clause: role is viewer. The builder generates the expr-lang
    // and the backend /validate endpoint confirms it compiles.
    await page.getByLabel('Value').first().fill('viewer');
    // Exact: seeded policy rows render the same expression plus `&& action !=
    // "read"`, so a substring match also hits those table cells.
    await expect(page.getByText('subject.role == "viewer"', { exact: true })).toBeVisible();
    await expect(page.getByText('✓ Valid')).toBeVisible({ timeout: 10000 });
  });

  test('Billing tab shows the plan-comparison catalog (F17)', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Billing & Usage' }).click();
    await expect(page.getByText('Compare plans')).toBeVisible({ timeout: 10000 });
    // The catalog renders every tier with an actionable CTA.
    await expect(page.getByText('Enterprise').first()).toBeVisible();
    await expect(page.getByRole('link', { name: 'Contact sales' }).first()).toBeVisible();
  });

  test('Usage & Quota tab shows per-signal consumption vs plan', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Usage & Quota' }).click();
    await expect(page.getByRole('heading', { name: 'Usage & Quota' })).toBeVisible({ timeout: 10000 });
    // Resolves to a real state: per-signal usage cards (Logs/Traces/…) or an
    // explicit empty state — both prove /api/v1/usage/series is wired.
    // Case-insensitive: the cards render the raw signal id ("logs", "traces")
    // and only *look* capitalised via CSS text-transform, which changes
    // rendering but not the DOM text getByText matches against.
    await expect(
      page
        .locator('main')
        .getByText(/logs|traces|no usage recorded this period yet/i)
        .first(),
    ).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Loading usage…')).toHaveCount(0, { timeout: 10000 });
  });

  test('Rate Limits tab lists seeded rules', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Rate Limits' }).click();
    await expect(page.getByText('search-burst-guard')).toBeVisible({ timeout: 10000 });
  });

  test('Audit Log tab lists recorded changes', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Audit Log' }).click();
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
  });

  test('Audit Log integrity can be verified (F20)', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Audit Log' }).click();
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    // The hash-chain verifier replays the trail server-side; the seeded log is
    // intact, so the status banner resolves to a tamper-evident result.
    await page.getByRole('button', { name: 'Verify integrity' }).click();
    await expect(page.getByRole('status')).toContainText(/tamper-evident|verified|integrity check/i, { timeout: 10000 });
  });

  test('Security tab offers MFA enrolment (F18)', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Security (MFA)' }).click();
    await expect(page.getByRole('heading', { name: 'Two-Factor Authentication' })).toBeVisible({ timeout: 10000 });
    // The seeded admin has no MFA, so the panel resolves to the not-enabled
    // state and offers enrolment — proving /api/v1/auth/mfa/status is wired.
    await expect(page.getByText('Not enabled')).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole('button', { name: 'Set up authenticator' })).toBeVisible();
    // The same tab hosts password change + active-session management (F18).
    await expect(page.getByRole('heading', { name: 'Change Password' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Active Sessions' })).toBeVisible();
  });

  test('SSO tab shows configuration status', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'SSO / SAML' }).click();
    await expect(page.getByText('Google Workspace (OIDC)')).toBeVisible();
  });

  test('API Keys tab lists real keys (not a placeholder)', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'API Keys' }).click();
    // The real panel renders the seeded key and its non-secret prefix; the old
    // hardcoded placeholder ("Production Cluster Key") must be gone.
    await expect(page.locator('table tbody tr', { hasText: 'production-agents' }).first()).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('pt_ingest_', { exact: false }).first()).toBeVisible();
    await expect(page.getByText('Production Cluster Key')).toHaveCount(0);
  });

  test('Alert Channels: lists the seeded channel and adds a new one', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Alert Channels' }).click();
    // The real Channels panel (not the old env-only info block) shows the seeded
    // channel and an add control.
    await expect(page.getByText('demo-webhook').first()).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole('button', { name: '+ Add Channel' })).toBeVisible();

    const name = `e2e-slack-${Date.now()}`;
    await page.getByRole('button', { name: '+ Add Channel' }).click();
    await page.getByPlaceholder('e.g. oncall-slack').fill(name);
    // Default type is slack; fill its webhook URL and create.
    await page.getByPlaceholder('https://hooks.slack.com/services/…').fill('https://hooks.slack.com/services/T/B/x');
    await page.getByRole('button', { name: 'Create channel' }).click();
    await expect(page.getByText(name).first()).toBeVisible({ timeout: 10000 });
  });

  test('Anomalies tab loads the detection tuning', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Anomalies' }).click();
    await expect(page.getByRole('heading', { name: 'Anomaly Detection' })).toBeVisible();
    // Config loads (defaults when unset) — the enable toggle and a threshold field render.
    await expect(page.getByText('Anomaly detection enabled')).toBeVisible({ timeout: 10000 });
    await expect(page.getByLabel('Latency spike (× baseline)')).toBeVisible();
  });

  test('Data & Privacy tab gates deletion behind a type-to-confirm modal', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Data & Privacy' }).click();
    await expect(page.getByRole('heading', { name: 'Data & Privacy' })).toBeVisible();
    await expect(page.getByText('Purge telemetry data')).toBeVisible();
    // Exact: the section heading is "Close account" and its trigger is
    // "Close account…", so a substring match hits both.
    await expect(page.getByText('Close account', { exact: true })).toBeVisible();

    // Open the purge confirm — but never actually confirm (that would wipe the
    // shared seeded tenant). The confirm button stays disabled until an id is
    // typed; assert the gate, then cancel.
    await page.getByRole('button', { name: 'Purge data…' }).click();
    const dialog = page.getByRole('dialog');
    await expect(dialog.getByText(/Type your/i)).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Purge data', exact: true })).toBeDisabled();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });

  test('API Keys: full lifecycle — create reveals a one-time key, then rotate', async ({ page }) => {
    const keyName = `e2e-key-${Date.now()}`;
    await page.goto('/settings');
    await page.getByRole('button', { name: 'API Keys' }).click();

    // Create → the plaintext is revealed exactly once.
    await page.getByRole('button', { name: '+ Generate Key' }).click();
    await page.getByPlaceholder('e.g. production-agents').fill(keyName);
    await page.getByRole('button', { name: 'Generate', exact: true }).click();

    const reveal = page.getByRole('dialog');
    await expect(reveal).toBeVisible({ timeout: 10000 });
    await expect(reveal.getByText(/^pt_ingest_/)).toBeVisible();
    await reveal.getByRole('button', { name: 'Done' }).click();

    // The new key now appears in the table.
    const row = page.locator('table tbody tr', { hasText: keyName });
    await expect(row.first()).toBeVisible({ timeout: 10000 });

    // Rotate it with an explicit grace window → a fresh key is revealed.
    await row.first().getByRole('button', { name: 'Rotate' }).click();
    const rotateDialog = page.getByRole('dialog');
    await expect(rotateDialog).toBeVisible();
    await rotateDialog.getByLabel('Grace period').selectOption('1h');
    await rotateDialog.getByRole('button', { name: 'Rotate key' }).click();

    const rotateReveal = page.getByRole('dialog');
    await expect(rotateReveal.getByText(/rotated/i)).toBeVisible({ timeout: 10000 });
    await expect(rotateReveal.getByText(/^pt_ingest_/)).toBeVisible();
    await rotateReveal.getByRole('button', { name: 'Done' }).click();
  });
});
