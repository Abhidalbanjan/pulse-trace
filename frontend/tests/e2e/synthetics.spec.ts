import { test, expect } from '@playwright/test';

test.describe('Synthetic Monitoring', () => {
  test('shows KPI tiles and seeded checks', async ({ page }) => {
    await page.goto('/synthetics');
    await expect(page.getByText('Global Uptime')).toBeVisible();
    await expect(page.getByText('Active Checks')).toBeVisible();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('the seeded multi-step check is listed', async ({ page }) => {
    await page.goto('/synthetics');
    // The multi-step "Checkout journey" check the seed created must appear even
    // before its first probe result, via the /synthetics/tests listing.
    await expect(page.getByText('Checkout journey').first()).toBeVisible({ timeout: 10000 });
  });

  test('the check builder assembles a multi-step check with assertions', async ({ page }) => {
    await page.goto('/synthetics');
    await page.getByRole('button', { name: /create check/i }).click();
    await expect(page.getByRole('heading', { name: 'Create Synthetic Check' })).toBeVisible();

    await page.getByPlaceholder('e.g. Checkout flow').fill('E2E smoke');
    // Step 1: URL + assertions.
    await page.getByPlaceholder('https://api.acme.io/health').first().fill('https://example.com/health');
    await page.getByPlaceholder('2xx').first().fill('200');

    // Add a second step — proving multi-step.
    await page.getByRole('button', { name: '+ Add step' }).click();
    await expect(page.getByText('STEP 2')).toBeVisible();
    await page.getByPlaceholder('https://api.acme.io/health').nth(1).fill('https://example.com/ready');

    await page.getByRole('button', { name: /start monitoring/i }).click();
    // The modal closes on success and the new check appears in the table.
    await expect(page.getByRole('heading', { name: 'Create Synthetic Check' })).toBeHidden({ timeout: 10000 });
    await expect(page.getByText('E2E smoke').first()).toBeVisible({ timeout: 10000 });
  });
});
