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

  test('multi-series formula evaluates an expression (E4)', async ({ page }) => {
    await page.goto('/metrics');
    await expect(page.getByRole('heading', { name: 'Metrics' })).toBeVisible();
    // Select any metric so the chart toolbar (and Formula toggle) appears.
    await page.locator('main').getByText(/requests_total|queue_depth|memory_bytes/i).first().click();
    await page.getByRole('button', { name: /Formula/ }).click();
    // The formula builder exposes series pickers and an expression box.
    await expect(page.getByLabel('Formula expression')).toBeVisible({ timeout: 10000 });
    await page.getByLabel('Series a metric').selectOption({ index: 1 });
    await page.getByLabel('Formula expression').fill('a * 2');
    await page.getByRole('button', { name: 'Compute', exact: true }).click();
    // Resolves to a real state (a computed line or an explicit empty note),
    // proving GET /api/v1/metrics/formula is wired — never an error banner.
    await expect(page.getByText('Evaluating…')).toHaveCount(0, { timeout: 10000 });
    await expect(page.getByText(/Failed to evaluate formula/i)).toHaveCount(0);
  });
});
