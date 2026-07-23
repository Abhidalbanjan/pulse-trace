import { test, expect } from '@playwright/test';

test.describe('Synthetic Monitoring', () => {
  test('shows KPI tiles and seeded synthetic targets', async ({ page }) => {
    await page.goto('/synthetics');
    await expect(page.getByText('Global Uptime')).toBeVisible();
    await expect(page.getByText('Active Tests')).toBeVisible();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('create test modal opens and accepts a URL', async ({ page }) => {
    await page.goto('/synthetics');
    await page.getByRole('button', { name: /create test/i }).click();
    const urlInput = page.locator('input[type="text"], input[type="url"]').last();
    await expect(urlInput).toBeVisible();
    await urlInput.fill('https://example.com/health');
  });
});
