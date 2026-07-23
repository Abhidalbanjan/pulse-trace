import { test, expect } from '@playwright/test';

test.describe('Incidents', () => {
  test('lists seeded incidents and shows detail on click', async ({ page }) => {
    await page.goto('/incidents');
    await expect(page.getByText('Active Incidents')).toBeVisible();
    // IncidentsView builds its own title as "Incident in {service}" rather than
    // using the API's raw title field.
    const rows = page.locator('main').getByText(/Incident in/i);
    await expect(rows.first()).toBeVisible({ timeout: 10000 });
    await rows.first().click();
    await expect(page.getByText(/AI Root Cause Analysis|Causal Chain/i).first()).toBeVisible();
  });
});
