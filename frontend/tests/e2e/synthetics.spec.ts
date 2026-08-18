import { test, expect } from '@playwright/test';

test.describe('Synthetic Monitoring', () => {
  test('shows KPI tiles and seeded checks', async ({ page }) => {
    await page.goto('/synthetics');
    await expect(page.getByText('Global Uptime')).toBeVisible();
    await expect(page.getByText('Active Checks')).toBeVisible();
    await expect(page.locator('table')).toBeVisible({ timeout: 10000 });
    const rowCount = await page.locator('table tbody tr').count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('the seeded multi-step check is listed', async ({ page }) => {
    await page.goto('/synthetics');
    // The multi-step "Checkout journey" check the seed created must appear even
    // before its first probe result, via the /synthetics/tests listing.
    await expect(page.getByText('Checkout journey').first()).toBeVisible({ timeout: 10000 });
  });

  test('a configured name survives the prober, and an unnamed check is not named after its URL', async ({ page }) => {
    await page.goto('/synthetics');
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });

    // Regression: the list merges configured checks with probe observations, and
    // the prober stores `CheckName = url` for a check with no configured name.
    // Merging as `observation || config` therefore always short-circuited on
    // that URL, so a *renamed* check lost its name the moment it was first
    // probed. "Checkout journey" reuses the URL of a plain check registered
    // seconds earlier, so the previous test above passed or failed purely on
    // whether the prober had run yet — it was green by luck, not by behaviour.
    await expect(page.getByText('Checkout journey').first()).toBeVisible({ timeout: 10000 });

    // The same defect rendered the URL twice for unnamed checks — once as the
    // "name", once as the URL. A name that is the URL is not a name.
    const url = 'https://gateway.acme.io/status';
    const cell = page.getByRole('cell', { name: new RegExp(url.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')) }).first();
    await expect(cell).toBeVisible({ timeout: 10000 });
    const occurrences = (await cell.innerText()).split(url).length - 1;
    expect(occurrences).toBe(1);
  });

  test('expanding a check shows its 24h availability timeline', async ({ page }) => {
    await page.goto('/synthetics');
    await expect(page.locator('table tbody tr').first()).toBeVisible({ timeout: 10000 });
    // The uptime % in each row is a toggle; clicking it reveals the SLA strip.
    await page.getByRole('button', { name: /Show the 24h availability timeline/ }).first().click();
    await expect(page.getByText(/AVAILABILITY · LAST 24 HOURS/i)).toBeVisible({ timeout: 10000 });
    // Resolves to a real state (strip or explicit empty), never an infinite spinner.
    await expect(page.getByText('Loading timeline…')).toHaveCount(0, { timeout: 10000 });
  });

  test('the check builder assembles a multi-step check with assertions', async ({ page }) => {
    await page.goto('/synthetics');
    await page.getByRole('button', { name: /create check/i }).click();
    await expect(page.getByRole('heading', { name: 'Create Synthetic Check' })).toBeVisible();

    await page.getByPlaceholder('e.g. Checkout flow').fill('E2E smoke');
    // Step 1: URL + assertions.
    await page.getByPlaceholder('https://api.acme.io/health').first().fill('https://example.com/health');
    await page.getByPlaceholder('2xx').first().fill('200');

    // Add a second step — proving multi-step.
    await page.getByRole('button', { name: '+ Add step' }).click();
    await expect(page.getByText('STEP 2')).toBeVisible();
    await page.getByPlaceholder('https://api.acme.io/health').nth(1).fill('https://example.com/ready');

    await page.getByRole('button', { name: /start monitoring/i }).click();
    // The modal closes on success and the new check appears in the table.
    await expect(page.getByRole('heading', { name: 'Create Synthetic Check' })).toBeHidden({ timeout: 10000 });
    await expect(page.getByText('E2E smoke').first()).toBeVisible({ timeout: 10000 });
  });
});
