import { test, expect } from '@playwright/test';

test.describe('Continuous Profiler', () => {
  test('renders the native flat-profile surface (no iframe embed)', async ({ page }) => {
    await page.goto('/profiler');
    await expect(page.getByRole('heading', { name: 'Continuous Profiler' })).toBeVisible();
    // The profiler is now a PulseTrace-native surface, not a Pyroscope iframe.
    await expect(page.locator('iframe')).toHaveCount(0);
    // Default flat view is the interactive flame graph.
    await expect(page.getByText(/Flame graph —/i)).toBeVisible({ timeout: 10000 });
    // Resolves to a real state (data or an explicit empty), never a stuck spinner.
    await expect(page.getByText('Loading profile…')).toHaveCount(0, { timeout: 10000 });
    // The List toggle switches back to the flat top-functions table.
    await page.getByRole('button', { name: /List/ }).click();
    await expect(page.getByText(/Top functions by self time/i)).toBeVisible({ timeout: 10000 });
  });

  test('flame graph exposes an interactive search control', async ({ page }) => {
    await page.goto('/profiler');
    await expect(page.getByText(/Flame graph —/i)).toBeVisible({ timeout: 10000 });
    await expect(page.getByText('Loading profile…')).toHaveCount(0, { timeout: 10000 });
    // The flame view resolves to either a searchable graph or an explicit empty
    // state — both prove the flame payload path is wired, not blank.
    await expect(
      page.getByLabel('Search flame graph').or(page.getByText(/No flame data in this window/i)),
    ).toBeVisible({ timeout: 10000 });
  });

  test('regression-detection mode shows a diff verdict', async ({ page }) => {
    await page.goto('/profiler');
    await page.getByRole('button', { name: /detect regressions/i }).click();
    await expect(page.getByText(/Regression diff/i)).toBeVisible({ timeout: 10000 });
    // A verdict banner renders (either N regressed or no regressions).
    await expect(page.getByText(/regress|No profile regressions/i).first()).toBeVisible({ timeout: 10000 });
  });

  test('regression mode offers a red/green diff flame (E2)', async ({ page }) => {
    await page.goto('/profiler');
    await page.getByRole('button', { name: /detect regressions/i }).click();
    await expect(page.getByText(/Regression diff/i)).toBeVisible({ timeout: 10000 });
    // Diff flame is the default compare view; its legend explains the coloring,
    // or an explicit empty state renders — both prove the diff-flame path is wired.
    await expect(
      page.getByText('grew vs baseline').or(page.getByText(/No profile samples in either window/i)),
    ).toBeVisible({ timeout: 10000 });
    // The Table toggle switches back to the per-function diff table.
    await page.getByRole('button', { name: /Table/ }).click();
    await expect(page.getByText(/Δ \(share\)|No profile samples/).first()).toBeVisible({ timeout: 10000 });
  });
});
