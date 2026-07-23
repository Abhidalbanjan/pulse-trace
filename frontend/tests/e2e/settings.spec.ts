import { test, expect } from '@playwright/test';

test.describe('Settings', () => {
  test('Users tab lists seeded users', async ({ page }) => {
    await page.goto('/settings');
    await expect(page.getByText('User Management')).toBeVisible();
    // Scope to the content panel: the sidebar footer always shows the logged-in
    // user's own name, which can collide with a seeded username/role string.
    await expect(page.locator('main').getByText('sarah.oncall').first()).toBeVisible({ timeout: 10000 });
  });

  test('Roles tab lists seeded roles with permissions', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Roles (RBAC)' }).click();
    // Scope to the roles table row, not page-wide: the sidebar footer also
    // renders the current user's role ("admin"), which would otherwise collide.
    await expect(page.locator('table tbody tr', { hasText: 'admin' }).first()).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('support')).toBeVisible();
  });

  test('Policies tab lists seeded ABAC policies', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Policies (ABAC)' }).click();
    await expect(page.getByText('viewer-write-block')).toBeVisible({ timeout: 10000 });
  });

  test('Rate Limits tab lists seeded rules', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Rate Limits' }).click();
    await expect(page.getByText('search-burst-guard')).toBeVisible({ timeout: 10000 });
  });

  test('Audit Log tab lists recorded changes', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'Audit Log' }).click();
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
  });

  test('SSO tab shows configuration status', async ({ page }) => {
    await page.goto('/settings');
    await page.getByRole('button', { name: 'SSO / SAML' }).click();
    await expect(page.getByText('Google Workspace (OIDC)')).toBeVisible();
  });
});
