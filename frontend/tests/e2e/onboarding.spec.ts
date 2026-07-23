import { test, expect } from '@playwright/test';

test.describe('Onboarding wizard', () => {
  test('walks through all 3 steps and generates an install script', async ({ page }) => {
    await page.goto('/onboarding');
    await expect(page.getByText('Welcome to PulseTrace.')).toBeVisible();

    await page.getByText('Kubernetes', { exact: true }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await expect(page.getByText('Generate Ingestion Key')).toBeVisible();
    await page.getByRole('button', { name: 'Generate API Key' }).click();

    await expect(page.getByText("You're all set!")).toBeVisible({ timeout: 10000 });
    await expect(page.locator('pre')).toContainText('helm install');
    await expect(page.getByText(/waiting for data/i)).toBeVisible();
  });
});
