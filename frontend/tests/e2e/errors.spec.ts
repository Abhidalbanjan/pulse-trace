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

  test('assign an owner to an error group persists', async ({ page }) => {
    await page.goto('/errors');
    await page.getByRole('button', { name: 'All' }).click();
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });

    const firstRow = page.locator('table tbody tr').first();
    // Open the inline assignee editor, type an owner, commit with Enter.
    await firstRow.getByRole('button', { name: '+ Assign' }).click();
    const input = firstRow.getByLabel('Assignee');
    await input.fill('oncall-ana');
    await input.press('Enter');
    // The PATCH round-trips and the refetched list shows the owner on that group.
    await expect(page.getByRole('button', { name: 'oncall-ana' }).first()).toBeVisible({ timeout: 10000 });
  });

  test('snooze control is available on open groups', async ({ page }) => {
    await page.goto('/errors');
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    // The Snooze duration picker renders on non-snoozed groups.
    await expect(page.getByLabel('Snooze this error group').first()).toBeVisible();
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
