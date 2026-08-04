import { test, expect } from '@playwright/test';

test.describe('SLOs', () => {
  test('renders the SLO dashboard with the seeded objective', async ({ page }) => {
    await page.goto('/slo');
    await expect(page.getByRole('heading', { name: 'Service Level Objectives' })).toBeVisible();
    // Seed provisions an SLO for payment-service; its card + budget gauge render.
    await expect(page.getByText('payment-service').first()).toBeVisible({ timeout: 15000 });
    await expect(page.getByText('Error budget remaining').first()).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Budget Alerts' })).toBeVisible();
  });

  test('creates a new SLO', async ({ page }) => {
    const svc = `e2e-svc-${Date.now()}`;
    await page.goto('/slo');
    await page.getByRole('button', { name: '+ New SLO' }).click();
    await page.getByPlaceholder('e.g. payment-service').fill(svc);
    await page.getByRole('button', { name: 'Create SLO' }).click();
    await expect(page.getByText(svc).first()).toBeVisible({ timeout: 15000 });
  });
});
