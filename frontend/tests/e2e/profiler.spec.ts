import { test, expect } from '@playwright/test';

test.describe('Continuous Profiler', () => {
  test('renders toolbar and embeds the flame graph iframe', async ({ page }) => {
    await page.goto('/profiler');
    await expect(page.getByRole('heading', { name: 'Continuous Profiler' })).toBeVisible();
    await expect(page.locator('iframe')).toBeVisible({ timeout: 10000 });
  });
});
