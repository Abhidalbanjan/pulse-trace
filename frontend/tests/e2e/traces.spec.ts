import { test, expect } from '@playwright/test';

test.describe('Distributed Traces', () => {
  // The default tab is now the first-class ClickHouse "Trace Search" (Traces · E1);
  // the Jaeger-backed explorer lives behind the "Trace Explorer" tab.
  const openExplorer = async (page: import('@playwright/test').Page) => {
    await page.goto('/traces');
    await page.getByRole('button', { name: 'Trace Explorer' }).click();
  };

  test('Trace Search tab exposes the first-class filter bar (E1)', async ({ page }) => {
    await page.goto('/traces');
    // Search is the default tab: the APM filter bar is present.
    await expect(page.getByLabel('Service')).toBeVisible({ timeout: 10000 });
    await expect(page.getByLabel('Status')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Search', exact: true })).toBeVisible();
    // Running a search resolves to a concrete state (results table or empty note),
    // proving GET /api/v1/traces is wired end-to-end.
    await page.getByRole('button', { name: 'Search', exact: true }).click();
    await expect(page.locator('table').or(page.getByText(/No traces match|Set filters/i)).first()).toBeVisible({ timeout: 10000 });
  });

  test('lists seeded traces for a service', async ({ page }) => {
    await openExplorer(page);
    await expect(page.locator('table')).toBeVisible({ timeout: 15000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('clicking a trace opens the waterfall view', async ({ page }) => {
    await openExplorer(page);
    await page.waitForTimeout(1000);
    const firstRow = page.locator('table tbody tr').first();
    await expect(firstRow).toBeVisible({ timeout: 10000 });
    await firstRow.click();
    await expect(page.getByRole('button', { name: /back to traces/i })).toBeVisible();
  });

  test('trace→logs pivot jumps to the Explorer scoped to the trace', async ({ page }) => {
    await openExplorer(page);
    await page.waitForTimeout(1000);
    const firstRow = page.locator('table tbody tr').first();
    await expect(firstRow).toBeVisible({ timeout: 10000 });
    await firstRow.click();
    await expect(page.getByRole('button', { name: /back to traces/i })).toBeVisible();
    // Select a span to reveal the correlated-logs panel, then pivot to Explorer.
    // Span bars render their label as "service : operation" (the only " : "
    // text on screen before a span is selected).
    await page.getByText(/Select a span/i).waitFor({ state: 'visible' });
    await page.getByText(/ : /).first().click();
    const openInExplorer = page.getByRole('button', { name: /open in explorer/i });
    await expect(openInExplorer).toBeVisible({ timeout: 10000 });
    await openInExplorer.click();
    await expect(page).toHaveURL(/\/explorer\?q=.*trace_id/);
  });
});
