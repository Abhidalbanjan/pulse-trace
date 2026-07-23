import { test, expect } from '@playwright/test';

test.describe('Deployment Gates', () => {
  test('page renders (data source is a stub, expect empty state not a crash)', async ({ page }) => {
    await page.goto('/deployments');
    await expect(page.getByText('Shift-Left Deployment Gates')).toBeVisible();
    await expect(page.getByRole('button', { name: /configure webhooks/i })).toBeVisible();
  });
});
