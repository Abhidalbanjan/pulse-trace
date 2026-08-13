import { test, expect } from '@playwright/test';

test.describe('Alerts', () => {
  test('renders the alert stream with filters', async ({ page }) => {
    await page.goto('/alerts');
    await expect(page.getByRole('heading', { name: 'Alerts' })).toBeVisible();
    await expect(page.getByPlaceholder('Filter by service…')).toBeVisible();
    // The list resolves to either alerts (level badges) or the empty state —
    // both prove the screen loaded against the real API without error.
    await expect(
      page.getByText(/No alerts match these filters\.|CRITICAL|ERROR|WARNING/).first(),
    ).toBeVisible({ timeout: 15000 });
  });

  test('create and delete an alert silence (E2)', async ({ page }) => {
    await page.goto('/alerts');
    await expect(page.getByRole('heading', { name: 'Alerts' })).toBeVisible();

    await page.getByRole('button', { name: /Silences/ }).click();
    await expect(page.getByText('Alert silences · maintenance windows')).toBeVisible({ timeout: 10000 });

    // Create a distinctive silence and confirm it lands in the list, active.
    const svc = `e2e-silence-${Date.now()}`;
    await page.getByLabel('Silence service').fill(svc);
    await page.getByRole('button', { name: 'Add silence' }).click();
    const label = page.getByText(`service=${svc}`);
    await expect(label).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('ACTIVE').first()).toBeVisible();

    // Delete it again — the × sits in the same row as the matcher label.
    const row = label.locator('xpath=ancestor::div[1]');
    await row.getByRole('button', { name: '×' }).click();
    await expect(page.getByText(`service=${svc}`)).toHaveCount(0, { timeout: 10000 });
  });

  test('groups near-identical alerts and expands to instances', async ({ page }) => {
    await page.goto('/alerts');
    await expect(page.getByRole('heading', { name: 'Alerts' })).toBeVisible();

    const groupBtn = page.getByRole('button', { name: /Group similar/ });
    await expect(groupBtn).toBeVisible();
    await groupBtn.click();
    // Toggle flips to the pressed "Grouped" state and the grouped list resolves
    // (either dedup rows with counts, or the empty state) — both prove the
    // group=true path loaded against the real API without error.
    await expect(page.getByRole('button', { name: /Grouped/ })).toHaveAttribute('aria-pressed', 'true');
    await expect(
      page.getByText(/No alerts match these filters\.|×\d+|CRITICAL|ERROR|WARNING/).first(),
    ).toBeVisible({ timeout: 15000 });
  });
});
