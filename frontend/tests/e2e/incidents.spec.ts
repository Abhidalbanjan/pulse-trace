import { test, expect } from '@playwright/test';

test.describe('Incidents', () => {
  test('lists incidents and shows causal detail + remediation for the first', async ({ page }) => {
    await page.goto('/incidents');
    await expect(page.getByText('Active Incidents')).toBeVisible();
    // The first incident auto-selects; its detail loads the real causal analysis
    // (no more hardcoded root-cause string).
    await expect(page.getByText('AI Root Cause Analysis')).toBeVisible({ timeout: 15000 });
    // The real remediation panel replaces the old hardcoded "Suggested Runbooks".
    await expect(page.getByRole('heading', { name: 'Remediation' })).toBeVisible();
    await expect(page.getByText('Suggested Runbooks')).toHaveCount(0);
    await expect(page.getByText('Restart Postgres Pool')).toHaveCount(0);
  });

  test('generates an AI-drafted postmortem (Incidents E1)', async ({ page }) => {
    await page.goto('/incidents');
    await expect(page.getByText('AI Root Cause Analysis')).toBeVisible({ timeout: 15000 });
    await page.getByRole('button', { name: 'Postmortem' }).click();
    // Generate (LLM when configured, deterministic template otherwise — always
    // works). Idempotent: once a draft exists the panel swaps the trigger to
    // "Regenerate", so a re-run against the same incident must accept either.
    await page
      .getByRole('button', { name: /Generate postmortem/i })
      .or(page.getByRole('button', { name: 'Regenerate' }))
      .first()
      .click();
    // The drafted document carries the structured sections.
    await expect(page.getByRole('heading', { name: 'Summary' })).toBeVisible({ timeout: 20000 });
    await expect(page.getByRole('heading', { name: 'Action Items' })).toBeVisible();
    // Edit + Export controls appear once a draft exists.
    await expect(page.getByRole('button', { name: 'Export' })).toBeVisible();
  });

  test('surfaces causal-AI provider health next to the analysis (F15)', async ({ page }) => {
    await page.goto('/incidents');
    await expect(page.getByText('AI Root Cause Analysis')).toBeVisible({ timeout: 15000 });
    // The provider-health badge always resolves to a concrete state: a live/backup
    // LLM ("Causal AI: <provider>") or the deterministic engine ("Rule-based
    // analyzer") — proving GET /api/v1/causal/providers is wired, not blank.
    await expect(page.getByText(/Causal AI:|Rule-based analyzer/).first()).toBeVisible({ timeout: 10000 });
  });

  test('remediation panel is policy-aware and supports dry-run when a playbook exists', async ({ page }) => {
    await page.goto('/incidents');
    await expect(page.getByText('AI Root Cause Analysis')).toBeVisible({ timeout: 15000 });

    // Either the selected incident has a proposed playbook (a Dry-run control is
    // offered) or it doesn't (the panel shows the honest empty state). Both are
    // valid; assert one holds rather than depending on the async causal pipeline
    // having produced a playbook for the first incident.
    const dryRun = page.getByRole('button', { name: 'Dry-run' });
    const emptyState = page.getByText(/No remediation has been proposed/i);
    await expect(dryRun.or(emptyState).first()).toBeVisible();

    if (await dryRun.count()) {
      await dryRun.first().click();
      // Dry-run is read-only and must never fail the page; a result surfaces.
      await expect(page.getByText(/Dry-run complete|plan below/i).first()).toBeVisible({ timeout: 10000 });
    }
  });
});
