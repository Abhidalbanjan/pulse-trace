import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// Accessibility baseline (ROAD_TO_100 · F0.4).
//
// Scans key screens with axe-core against the WCAG 2.1 A/AA rule set and fails on
// any *critical* violation. Critical is the initial bar — high-signal, low-noise —
// and is meant to be tightened to 'serious' as the app's a11y debt is burned down.
// The full violation list is attached to the report so 'serious'/'moderate' issues
// are visible without yet failing the build.

const WCAG_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'];

async function scan(page: import('@playwright/test').Page, testInfo: import('@playwright/test').TestInfo) {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();

  await testInfo.attach('axe-violations.json', {
    body: JSON.stringify(results.violations, null, 2),
    contentType: 'application/json',
  });

  const critical = results.violations.filter((v) => v.impact === 'critical');
  const summary = critical
    .map((v) => `${v.id} (${v.nodes.length}): ${v.help}`)
    .join('\n');
  expect(critical, `Critical a11y violations:\n${summary}`).toEqual([]);
}

test.describe('Accessibility baseline (authenticated)', () => {
  const PAGES: [string, string][] = [
    ['dashboard', '/'],
    ['settings', '/settings'],
    ['incidents', '/incidents'],
    ['explorer', '/explorer'],
  ];

  for (const [name, path] of PAGES) {
    test(`no critical a11y violations on ${name}`, async ({ page }, testInfo) => {
      await page.goto(path);
      // Let the screen's first data load settle so axe sees the real DOM.
      await page.waitForLoadState('networkidle');
      await scan(page, testInfo);
    });
  }
});

test.describe('Accessibility baseline (login)', () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test('no critical a11y violations on login', async ({ page }, testInfo) => {
    await page.goto('/login');
    await scan(page, testInfo);
  });
});
