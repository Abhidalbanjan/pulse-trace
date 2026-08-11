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

  test('forgot-password flow gives a uniform acknowledgement (F18)', async ({ page }) => {
    await page.goto('/login');
    await page.getByRole('button', { name: 'Forgot password?' }).click();
    await page.getByLabel('Email or Username').fill('admin');
    await page.getByRole('button', { name: 'Send reset link' }).click();
    // The response never reveals whether the account exists.
    await expect(page.getByText(/reset link is on its way/i)).toBeVisible({ timeout: 10000 });
  });

  test('reset-password page prompts for a new password', async ({ page }) => {
    await page.goto('/reset-password?token=some-token');
    await expect(page.getByRole('heading', { name: 'Set a new password' })).toBeVisible();
    await expect(page.getByLabel('New password')).toBeVisible();
  });

  test('login offers OIDC and SAML SSO federation (F18)', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByRole('button', { name: /Continue with Google/i })).toBeVisible();
    await expect(page.getByRole('button', { name: /Continue with SAML SSO/i })).toBeVisible();
  });
});
