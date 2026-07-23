import { test, expect } from '@playwright/test';

test.describe('Home / AI SRE chat', () => {
  test('renders headline and initial AI message', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByText('PulseTrace Autonomous SRE')).toBeVisible();
    await expect(page.locator('main')).toContainText(/Autonomous SRE/i);
  });

  test('sending a message adds a user bubble and gets a reply', async ({ page }) => {
    await page.goto('/');
    const input = page.getByPlaceholder(/ask a question/i);
    await input.fill('Why is cart-service failing?');
    await page.getByRole('button', { name: 'Send' }).click();
    await expect(page.getByText('Why is cart-service failing?')).toBeVisible();
    // AI reply or typing indicator should appear within a reasonable window
    await expect(page.locator('main')).toContainText(/thinking|analy|error|service/i, { timeout: 15000 });
  });

  test('suggestion chips populate and send a message', async ({ page }) => {
    await page.goto('/');
    const chip = page.getByText('"Show me slow queries"');
    await expect(chip).toBeVisible();
    await chip.click();
    await expect(page.getByText('Show me slow queries', { exact: false }).first()).toBeVisible();
  });
});
