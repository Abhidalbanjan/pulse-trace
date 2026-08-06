import { test, expect } from '@playwright/test';

test.describe('Metrics', () => {
  test('lists the seeded metric catalog', async ({ page }) => {
    await page.goto('/metrics');
    await expect(page.getByRole('heading', { name: 'Metrics' })).toBeVisible();
    // The seed emits an OTLP gauge + counter per service; the catalog should
    // surface at least one of them.
    await expect(page.locator('main')).toContainText(/requests_total|queue_depth|memory_bytes/i, { timeout: 15000 });
  });

  test('applies a query function (rate) to the selected metric', async ({ page }) => {
    await page.goto('/metrics');
    // Pick the monotonic counter, for which rate() is meaningful.
    await page.getByText('requests_total').first().click();
    const fnSelect = page.getByLabel('Aggregation function');
    await expect(fnSelect).toBeVisible({ timeout: 10000 });
    // The full function allowlist is offered, including rate and percentiles.
    await expect(fnSelect.getByRole('option', { name: 'rate' })).toBeAttached();
    await expect(fnSelect.getByRole('option', { name: 'p95' })).toBeAttached();
    await fnSelect.selectOption('rate');
    await expect(fnSelect).toHaveValue('rate');
    // Switching the function must not error the chart panel.
    await expect(page.locator('main')).not.toContainText(/Failed to load metric data/i);
  });
});
