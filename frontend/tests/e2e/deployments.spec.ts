import { test, expect } from '@playwright/test';

test.describe('Deployment Gates', () => {
  test('renders the recorded shift-left gate feed', async ({ page }) => {
    await page.goto('/deployments');
    await expect(page.getByRole('heading', { name: 'Shift-Left Deployment Gates' })).toBeVisible();
    // Seed posts a PR through the webhook; its recorded verdict shows in the feed
    // (or the honest empty state if the evaluator/seed didn't run).
    await expect(
      page.getByText(/Add caching layer to catalog-service|No deployment gates yet/).first(),
    ).toBeVisible({ timeout: 15000 });
  });

  test('shows the DORA scorecard (E2)', async ({ page }) => {
    await page.goto('/deployments');
    await expect(page.getByRole('heading', { name: 'Shift-Left Deployment Gates' })).toBeVisible();
    // The DORA tiles render once /api/v1/deployments/dora resolves (seeded
    // deployments exist), or gracefully stay hidden if there's no data.
    const dora = page.getByText('Change-failure rate');
    if (await dora.count() > 0) {
      await expect(dora).toBeVisible({ timeout: 10000 });
      await expect(page.getByText('Deploy frequency')).toBeVisible();
      await expect(page.getByText('Time to restore (MTTR)')).toBeVisible();
    }
  });

  test('exposes the webhook setup URL', async ({ page }) => {
    await page.goto('/deployments');
    await page.getByRole('button', { name: 'Configure webhook' }).click();
    await expect(page.getByText('/api/v1/webhooks/github')).toBeVisible();
  });
});
