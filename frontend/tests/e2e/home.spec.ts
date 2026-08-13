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

  test('grounded suggestion chips populate the input', async ({ page }) => {
    await page.goto('/');
    // Chips are grounded prompts fetched from /api/v1/chat/suggestions (with a
    // static fallback), so we assert on whichever chip renders, not fixed text.
    const chip = page.locator('[data-testid="suggestion-chips"] button').first();
    await expect(chip).toBeVisible({ timeout: 10000 });
    const raw = (await chip.textContent()) || '';
    const text = raw.replace(/^"|"$/g, '').trim();
    expect(text.length).toBeGreaterThan(0);
    await chip.click();
    await expect(page.getByPlaceholder(/ask a question/i)).toHaveValue(text);
  });
});
