import { test, expect } from '@playwright/test';

test.use({ storageState: { cookies: [], origins: [] } });

test.describe('Auth', () => {
  test('unauthenticated user is redirected to /login', async ({ page }) => {
    await page.goto('/topology');
    await expect(page).toHaveURL(/\/login$/);
  });

  test('login with username (no @) succeeds', async ({ page }) => {
    await page.goto('/login');
    await page.getByPlaceholder('admin').fill('admin');
    await page.locator('input[type="password"]').fill('admin');
    await page.getByRole('button', { name: 'Sign In' }).click();
    await expect(page).toHaveURL('/');
    await expect(page.getByText('PulseTrace Autonomous SRE')).toBeVisible();
  });

  test('login with wrong password shows an error', async ({ page }) => {
    await page.goto('/login');
    await page.getByPlaceholder('admin').fill('admin');
    await page.locator('input[type="password"]').fill('wrong-password');
    await page.getByRole('button', { name: 'Sign In' }).click();
    await expect(page.getByText(/authentication failed|unauthorized|invalid/i)).toBeVisible({ timeout: 10000 });
    await expect(page).toHaveURL(/\/login$/);
  });
});
