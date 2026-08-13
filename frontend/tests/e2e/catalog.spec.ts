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

    await page.getByLabel('Lifecycle').selectOption('production');
    await page.getByLabel('Tier').selectOption('tier-1');
    await page.getByRole('button', { name: 'Save Metadata' }).click();

    // Modal closes and the row now reflects the production lifecycle badge.
    await expect(page.getByRole('heading', { name: 'Service Metadata' })).toHaveCount(0, { timeout: 10000 });
    await expect(row.getByText('Production')).toBeVisible({ timeout: 10000 });
  });

  test('search filters the catalog', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    await page.getByLabel('Search services').fill('payment');
    await expect(page.locator('table tbody tr', { hasText: 'payment-service' }).first()).toBeVisible();
    await expect(page.locator('table tbody tr', { hasText: 'cart-service' })).toHaveCount(0);
  });
});
