import { test, expect } from '@playwright/test';

test.describe('Service Catalog', () => {
  test('lists seeded catalog entries with team/repo/slack', async ({ page }) => {
    await page.goto('/catalog');
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Team Checkout')).toBeVisible();
    await expect(page.getByText('#eng-checkout')).toBeVisible();
  });
});
