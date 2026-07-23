import { test, expect } from '@playwright/test';

test.describe('Service detail', () => {
  test('cart-service detail page shows seeded deployments', async ({ page }) => {
    await page.goto('/services/cart-service');
    await expect(page.locator('main')).toContainText(/cart-service/i);
    // Two deployments were seeded for cart-service with identical notes text.
    await expect(page.getByText(/Seeded deployment for demo data/i).first()).toBeVisible({ timeout: 10000 });
  });
});
