import { test, expect } from '@playwright/test';

test.describe('Services', () => {
  test('lists services with health/latency data', async ({ page }) => {
    await page.goto('/services');
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('health column renders scores and sorts', async ({ page }) => {
    await page.goto('/services');
    await expect(page.getByRole('columnheader', { name: /Health/ })).toBeVisible({ timeout: 10000 });
    // Each service shows a health band badge (healthy/degraded/unhealthy/critical).
    await expect(page.locator('table tbody tr').first().getByText(/healthy|degraded|unhealthy|critical/i)).toBeVisible({ timeout: 10000 });
    // The Health header is a sort toggle.
    await page.getByRole('columnheader', { name: /Health/ }).click();
    await expect(page.getByRole('columnheader', { name: /Health ▲/ })).toBeVisible();
  });

  test('clicking a service navigates to its detail page', async ({ page }) => {
    await page.goto('/services');
    const firstRow = page.locator('table tbody tr').first();
    await expect(firstRow).toBeVisible({ timeout: 10000 });
    await firstRow.click();
    await expect(page).toHaveURL(/\/services\/.+/);
  });
});
