import { test, expect } from '@playwright/test';

test.describe('Service Catalog', () => {
  test('lists seeded catalog entries with team/repo/slack', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Team Checkout')).toBeVisible();
    await expect(page.getByText('#eng-checkout')).toBeVisible();
  });

  test('carries an SLO budget scorecard column', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    await expect(page.getByRole('columnheader', { name: 'SLO Budget' })).toBeVisible();
    // Each row's SLO cell resolves to a real state — a budget % (seeded SLO) or
    // an explicit "No SLO" — proving the scorecard column is wired, not blank.
    const row = page.locator('table tbody tr', { hasText: 'payment-service' }).first();
    await expect(row).toContainText(/%|No SLO/, { timeout: 10000 });
  });

  test('edit rich metadata (lifecycle / tier / links) persists', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.getByRole('columnheader', { name: 'Lifecycle / Tier' })).toBeVisible({ timeout: 10000 });

    const row = page.locator('table tbody tr', { hasText: 'payment-service' }).first();
    // The lifecycle/tier cell is an edit button; open the metadata modal.
    await row.getByRole('button', { name: /Edit lifecycle, tier & links/ }).click();
    await expect(page.getByRole('heading', { name: 'Service Metadata' })).toBeVisible();

    // Exact: every row's edit button is labelled "Edit lifecycle, tier & links",
    // which substring-matches both "Lifecycle" and "Tier".
    await page.getByLabel('Lifecycle', { exact: true }).selectOption('production');
    await page.getByLabel('Tier', { exact: true }).selectOption('tier-1');
    await page.getByRole('button', { name: 'Save Metadata' }).click();

    // Modal closes and the row now reflects the production lifecycle badge.
    await expect(page.getByRole('heading', { name: 'Service Metadata' })).toHaveCount(0, { timeout: 10000 });
    await expect(row.getByText('Production')).toBeVisible({ timeout: 10000 });
  });

  test('expanding a service shows its dependencies (E4)', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    // The service name is a toggle that reveals upstream/downstream deps.
    const row = page.locator('table tbody tr', { hasText: 'payment-service' }).first();
    await row.getByRole('button', { name: /payment-service/ }).click();
    await expect(
      page.getByText('Depends on (upstream)').or(page.getByText('Loading dependencies…')),
    ).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Depends on (upstream)')).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Depended on by (downstream)')).toBeVisible();
  });

  test('search filters the catalog', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    await page.getByLabel('Search services').fill('payment');
    await expect(page.locator('table tbody tr', { hasText: 'payment-service' }).first()).toBeVisible();
    await expect(page.locator('table tbody tr', { hasText: 'cart-service' })).toHaveCount(0);
  });
});
