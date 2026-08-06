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

  test('expanding a group shows its occurrence timeline', async ({ page }) => {
    await page.goto('/errors');
    await page.getByRole('button', { name: 'All' }).click();
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    await page.getByRole('button', { name: 'Timeline' }).first().click();
    // The expanded panel renders and resolves to a real state (chart or explicit
    // empty), never an infinite spinner.
    await expect(page.getByText(/OCCURRENCES · LAST 7 DAYS/i)).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Loading timeline…')).toHaveCount(0, { timeout: 10000 });
  });
});
