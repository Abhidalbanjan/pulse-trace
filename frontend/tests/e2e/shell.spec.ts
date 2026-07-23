import { test, expect } from '@playwright/test';

const NAV_ITEMS: [string, string][] = [
  ['AI SRE', '/'],
  ['Incidents', '/incidents'],
  ['Deploy Gates', '/deployments'],
  ['Onboarding', '/onboarding'],
  ['Log Explorer', '/explorer'],
  ['Distributed Traces', '/traces'],
  ['Services', '/services'],
  ['Error Tracking', '/errors'],
  ['Continuous Profiler', '/profiler'],
  ['Real User Monitoring', '/rum'],
  ['Synthetic Monitoring', '/synthetics'],
  ['Topology', '/topology'],
  ['Catalog', '/catalog'],
  ['Settings', '/settings'],
];

test.describe('Global shell', () => {
  test('sidebar links navigate to every route', async ({ page }) => {
    await page.goto('/');
    for (const [label, path] of NAV_ITEMS) {
      // Nav links render a Material Symbols ligature glyph immediately before the
      // label text (e.g. "auto_awesome AI SRE"), so the accessible name includes
      // both - match on the label as a substring, not an exact match.
      await page.getByRole('link', { name: label }).click();
      await expect(page).toHaveURL(new RegExp(path === '/' ? '/$' : path.replace('/', '\\/') + '$'));
    }
  });

  test('theme toggle switches to dark and persists across reload', async ({ page }) => {
    await page.goto('/topology');
    const toggle = page.locator('button[title="Toggle theme"]');
    await toggle.click();
    const themeAfterToggle = await page.evaluate(() => localStorage.getItem('pt-theme'));
    expect(themeAfterToggle).toBe('dark');

    await page.reload();
    const themeAfterReload = await page.evaluate(() => localStorage.getItem('pt-theme'));
    expect(themeAfterReload).toBe('dark');

    // reset back to light so other tests in the run see a consistent default
    await page.locator('button[title="Toggle theme"]').click();
  });

  test('sidebar collapses and expands', async ({ page }) => {
    await page.goto('/topology');
    const sidebar = page.locator('aside');
    const expandedBox = await sidebar.boundingBox();
    await sidebar.locator('div').first().click();
    await page.waitForTimeout(400);
    const collapsedBox = await sidebar.boundingBox();
    expect(collapsedBox!.width).toBeLessThan(expandedBox!.width);
  });

  test('Material Symbols icon font actually loads (not rendering as literal text)', async ({ page }) => {
    await page.goto('/topology');
    const fontFamily = await page.evaluate(async () => {
      await document.fonts.ready;
      const el = document.querySelector('.material-symbols-outlined');
      return el ? getComputedStyle(el).fontFamily : null;
    });
    expect(fontFamily).toContain('Material Symbols Outlined');
    const loaded = await page.evaluate(async () => {
      await document.fonts.ready;
      let found = false;
      document.fonts.forEach((f) => {
        if (f.family.includes('Material Symbols Outlined') && f.status === 'loaded') found = true;
      });
      return found;
    });
    expect(loaded).toBe(true);
  });
});
