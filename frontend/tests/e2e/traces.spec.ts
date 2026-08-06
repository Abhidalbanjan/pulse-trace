import { test, expect } from '@playwright/test';

test.describe('Distributed Traces', () => {
  test('lists seeded traces for a service', async ({ page }) => {
    await page.goto('/traces');
    // Use whatever service the page defaults to rather than racing the async
    // service-list fetch with selectOption('cart-service') - the option may not
    // exist yet when selectOption runs, and its own actionability wait can eat
    // most of the test timeout before falling through.
    await expect(page.locator('table')).toBeVisible({ timeout: 15000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('clicking a trace opens the waterfall view', async ({ page }) => {
    await page.goto('/traces');
    await page.waitForTimeout(1000);
    const firstRow = page.locator('table tbody tr').first();
    await expect(firstRow).toBeVisible({ timeout: 10000 });
    await firstRow.click();
    await expect(page.getByRole('button', { name: /back to traces/i })).toBeVisible();
  });

  test('trace→logs pivot jumps to the Explorer scoped to the trace', async ({ page }) => {
    await page.goto('/traces');
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
