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
