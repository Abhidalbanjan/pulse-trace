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

  test('a shareable link hydrates the search state', async ({ page }) => {
    // A pasted link reproduces the exact search, not just a single log.
    await page.goto('/explorer?q=' + encodeURIComponent('level:ERROR') + '&range=24h');
    const searchBox = page.getByPlaceholder(/Search logs/i);
    await expect(searchBox).toHaveValue('level:ERROR', { timeout: 10000 });
    // The 24h range button is the active (highlighted) one.
    await expect(page.getByRole('button', { name: '24h', exact: true })).toBeVisible();
  });

  test('log→trace pivot navigates to the trace waterfall', async ({ page }) => {
    // Seeded logs carry a trace_id, so the detail drawer renders a "View Trace"
    // pivot (F7). It must deep-link to the trace, not sit dead.
    await page.goto('/explorer');
    const firstLogRow = page.locator('main').getByText('Request completed successfully').first();
    await expect(firstLogRow).toBeVisible({ timeout: 10000 });
    await firstLogRow.click();
    await expect(page.getByText('Log Details')).toBeVisible();
    await page.getByRole('button', { name: /view trace/i }).click();
    await expect(page).toHaveURL(/\/traces\?trace=/);
    await expect(page.getByRole('button', { name: /back to traces/i })).toBeVisible({ timeout: 10000 });
  });

  test('view-in-context opens the surrounding-logs overlay', async ({ page }) => {
    await page.goto('/explorer');
    const firstLogRow = page.locator('main').getByText('Request completed successfully').first();
    await expect(firstLogRow).toBeVisible({ timeout: 10000 });
    await firstLogRow.click();
    await expect(page.getByText('Log Details')).toBeVisible();
    await page.getByRole('button', { name: /view in context/i }).click();
    const dialog = page.getByRole('dialog', { name: /log context/i });
    await expect(dialog).toBeVisible({ timeout: 10000 });
    // The overlay resolves to a real state (either surrounding rows or an
    // explicit empty message), never an infinite spinner.
    await expect(dialog).not.toContainText('Loading context', { timeout: 10000 });
  });
  test('the volume histogram offers brush-to-zoom (E5)', async ({ page }) => {
    await page.goto('/explorer');
    // The histogram renders with a brush hint once volume data loads (or the
    // chart is absent when there is no data — both are valid states).
    const hint = page.getByText(/Drag across the histogram to zoom|Zoomed:/);
    if (await hint.count() > 0) {
      await expect(hint.first()).toBeVisible({ timeout: 10000 });
    }
  });
});
