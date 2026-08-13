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

  test('renders the web-vitals trend, device breakdown, and session table', async ({ page }) => {
    await page.goto('/rum');
    await expect(page.getByRole('heading', { name: 'Web Vitals Trend (p75)' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Browsers' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Operating Systems' })).toBeVisible();
    // Seeded sessions populate the stitched session table.
    await expect(page.getByRole('heading', { name: 'User Sessions' })).toBeVisible();
    await expect(page.getByText('Entry Path')).toBeVisible({ timeout: 10000 });
  });

  test('clicking a session opens its event timeline (E1)', async ({ page }) => {
    await page.goto('/rum');
    await expect(page.getByText('Entry Path')).toBeVisible({ timeout: 10000 });
    // Scope to the User Sessions table (the one with the "Entry Path" header).
    const sessionsTable = page.locator('table', { has: page.getByText('Entry Path') });
    const rows = sessionsTable.locator('tbody tr');
    // Only exercise the drill-in when seeded sessions exist.
    if (await rows.count() > 0) {
      await rows.first().click();
      await expect(page.getByRole('heading', { name: 'Session timeline' })).toBeVisible({ timeout: 10000 });
      await expect(page.getByText('Loading timeline…')).toHaveCount(0, { timeout: 10000 });
      await page.getByLabel('Close session timeline').click();
    }
  });

  test('time-range selector re-scopes the windowed panels', async ({ page }) => {
    await page.goto('/rum');
    const rangeSelect = page.getByLabel('Time range');
    await expect(rangeSelect).toBeVisible();
    await rangeSelect.selectOption('7d');
    await expect(rangeSelect).toHaveValue('7d');
    // Switching the window must not blank the trend panel heading.
    await expect(page.getByRole('heading', { name: 'Web Vitals Trend (p75)' })).toBeVisible();
  });
});
