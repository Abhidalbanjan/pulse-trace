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
});
