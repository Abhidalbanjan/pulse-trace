import { test, expect } from '@playwright/test';

test.describe('Topology', () => {
  test('renders the dependency graph with real nodes', async ({ page }) => {
    await page.goto('/topology');
    await expect(page.getByText('Interactive Service Topology')).toBeVisible();
    await expect(page.locator('.react-flow__node').first()).toBeVisible({ timeout: 10000 });
    const nodeCount = await page.locator('.react-flow__node').count();
    expect(nodeCount).toBeGreaterThan(0);
  });

  test('AI Root Cause button runs analysis and shows a result', async ({ page }) => {
    await page.goto('/topology');
    await page.locator('.react-flow__node').first().waitFor();
    await page.getByRole('button', { name: /AI Root Cause/i }).click();
    // either a narrative result panel or "no unhealthy services" message should show up
    await expect(page.locator('main')).toContainText(/root cause|no unhealthy|degraded/i, { timeout: 15000 });
  });

  test('search dims non-matching nodes and reset restores the view', async ({ page }) => {
    await page.goto('/topology');
    await page.locator('.react-flow__node').first().waitFor();
    const total = await page.locator('.react-flow__node').count();

    // Search by a seeded service name — matching nodes stay opaque, others dim
    // (dimmed, not removed) so spatial context holds.
    await page.getByLabel('Search services').fill('payment');
    await expect(page.locator('.react-flow__node')).toHaveCount(total);
    // At least one node is dimmed (reduced opacity) rather than hidden.
    await expect(page.locator('.react-flow__node[style*="opacity: 0.28"]').first()).toBeVisible({ timeout: 10000 });

    await page.getByRole('button', { name: /Reset view/i }).click();
    await expect(page.getByLabel('Search services')).toHaveValue('');
  });

  test('the health legend is shown for the overlay', async ({ page }) => {
    await page.goto('/topology');
    await expect(page.getByText('Healthy', { exact: true })).toBeVisible();
    await expect(page.getByText('Unhealthy', { exact: true })).toBeVisible();
  });
});
