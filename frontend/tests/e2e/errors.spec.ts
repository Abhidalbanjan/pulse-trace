import { test, expect } from '@playwright/test';

test.describe('Error Tracking', () => {
  test('lists seeded error groups with occurrence counts', async ({ page }) => {
    await page.goto('/errors');
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
    // occurrences column must render actual numbers, not blank/NaN (regression
    // guard for the ClickHouse UInt64-as-string decode bug)
    const firstOccurrenceCell = page.locator('table tbody tr').first().locator('td').nth(3);
    await expect(firstOccurrenceCell).not.toHaveText('');
    await expect(firstOccurrenceCell).not.toContainText('NaN');
  });

  test('status tabs filter the table', async ({ page }) => {
    await page.goto('/errors');
    await page.getByRole('button', { name: 'All' }).click();
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
  });
});
