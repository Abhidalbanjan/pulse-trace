import { test, expect } from '@playwright/test';

test.describe('Continuous Profiler', () => {
  test('renders the native flat-profile surface (no iframe embed)', async ({ page }) => {
    await page.goto('/profiler');
    await expect(page.getByRole('heading', { name: 'Continuous Profiler' })).toBeVisible();
    // The profiler is now a PulseTrace-native surface, not a Pyroscope iframe.
    await expect(page.locator('iframe')).toHaveCount(0);
    await expect(page.getByText(/Top functions by self time/i)).toBeVisible({ timeout: 10000 });
    // Resolves to a real state (data or an explicit empty), never a stuck spinner.
    await expect(page.getByText('Loading profile…')).toHaveCount(0, { timeout: 10000 });
  });

  test('regression-detection mode shows a diff verdict', async ({ page }) => {
    await page.goto('/profiler');
    await page.getByRole('button', { name: /detect regressions/i }).click();
    await expect(page.getByText(/Regression diff/i)).toBeVisible({ timeout: 10000 });
    // A verdict banner renders (either N regressed or no regressions).
    await expect(page.getByText(/regress|No profile regressions/i).first()).toBeVisible({ timeout: 10000 });
  });
});
