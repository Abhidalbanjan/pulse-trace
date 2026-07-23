import { test, expect } from '@playwright/test';

test.describe('Log Explorer', () => {
  test('shows seeded logs and facets', async ({ page }) => {
    await page.goto('/explorer');
    await expect(page.getByText('SERVICE NAME')).toBeVisible();
    await expect(page.locator('main')).toContainText(/cart-service|payment-service|gateway-service/i, { timeout: 10000 });
  });

  test('clicking a log row opens the detail drawer', async ({ page }) => {
    await page.goto('/explorer');
    // The LEVEL facet sidebar also renders text like "INFO" (as a filter option),
    // so a level-text selector alone can match a facet row instead of an actual
    // log row. Target a seeded log message directly instead - it only appears in
    // the log list.
    const firstLogRow = page.locator('main').getByText('Request completed successfully').first();
    await expect(firstLogRow).toBeVisible({ timeout: 10000 });
    await firstLogRow.click();
    await expect(page.getByText('Log Details')).toBeVisible();
    await expect(page.getByRole('button', { name: /view trace/i })).toBeVisible();
  });

  test('live tail toggle switches state', async ({ page }) => {
    await page.goto('/explorer');
    const liveToggle = page.getByText(/Live Tail/i);
    await expect(liveToggle).toBeVisible();
    await liveToggle.click();
    await expect(page.getByText(/Live Tail\s*On/i)).toBeVisible();
  });
});
