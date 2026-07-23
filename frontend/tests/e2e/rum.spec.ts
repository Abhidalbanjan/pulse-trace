import { test, expect } from '@playwright/test';

test.describe('Real User Monitoring', () => {
  test('renders core web vitals and session metrics', async ({ page }) => {
    await page.goto('/rum');
    await expect(page.getByText('Core Web Vitals')).toBeVisible();
    await expect(page.getByText('User Sessions')).toBeVisible();
    await expect(page.getByText(/Largest Contentful Paint/i)).toBeVisible();
  });

  test('shows recent JS errors table (seeded via real browser navigation)', async ({ page }) => {
    await page.goto('/rum');
    await expect(page.getByText('Recent JavaScript Errors')).toBeVisible();
  });
});
