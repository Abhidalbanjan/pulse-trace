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
});
