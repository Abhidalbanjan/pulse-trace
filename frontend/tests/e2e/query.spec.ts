import { test, expect } from '@playwright/test';

// The SQL workbench (P3.6). These assert the states that make the screen
// trustworthy, not just that it renders: a result that came from the engine, a
// refusal that explains itself, and a shared link that reproduces a statement.

test.describe('Query workbench', () => {
  test('runs a statement and reports where the work happened', async ({ page }) => {
    await page.goto('/query');

    const editor = page.getByTestId('sql-editor');
    await expect(editor).toBeVisible();
    await editor.fill('SELECT level, count(*) AS n FROM logs GROUP BY level ORDER BY n DESC');
    await page.getByTestId('run-query').click();

    // The grid renders the engine's columns, not ones the page invented.
    await expect(page.locator('th', { hasText: 'level' })).toBeVisible({ timeout: 20000 });
    await expect(page.getByTestId('result-rows').locator('tr').first()).toBeVisible();

    // A pushed-down aggregate moves no rows, and the footer says so — this is
    // the property that closed the benchmark class, surfaced to the user.
    await expect(page.getByTestId('result-summary')).toBeVisible();
    await expect(page.locator('footer')).toContainText(/answered in the store|rows scanned/);
  });

  test('the schema sidebar lists what the engine will accept', async ({ page }) => {
    await page.goto('/query');
    const sidebar = page.getByTestId('schema-sidebar');
    await expect(sidebar).toContainText('logs', { timeout: 15000 });
    await expect(sidebar).toContainText('incidents');
    // Attributes are advertised by shape, since their keys are the customer's.
    await expect(sidebar).toContainText('metadata.');
  });

  test('a refused statement explains the rule rather than just failing', async ({ page }) => {
    await page.goto('/query');
    await page.getByTestId('sql-editor').fill('DELETE FROM logs');
    await page.getByTestId('run-query').click();

    const alert = page.getByTestId('refusal');
    await expect(alert).toBeVisible({ timeout: 20000 });
    await expect(alert).toContainText(/read-only|only SELECT/i);
    await expect(page.getByTestId('refusal-message')).toBeVisible();
  });

  test('an unknown relation points the user at the catalog', async ({ page }) => {
    await page.goto('/query');
    await page.getByTestId('sql-editor').fill('SELECT * FROM not_a_real_table');
    await page.getByTestId('run-query').click();
    await expect(page.getByTestId('refusal')).toContainText(/not in the catalog/i, { timeout: 20000 });
  });

  test('a shared link reproduces the statement', async ({ page }) => {
    const sql = 'SELECT count(*) AS n FROM logs';
    await page.goto('/query?sql=' + encodeURIComponent(sql));
    await expect(page.getByTestId('sql-editor')).toHaveValue(sql);
  });

  test('suggestions offer catalog names and insert them', async ({ page }) => {
    await page.goto('/query');
    const editor = page.getByTestId('sql-editor');
    // Wait for the catalog before typing, so the list is populated.
    await expect(page.getByTestId('schema-sidebar')).toContainText('logs', { timeout: 15000 });

    await editor.fill('SELECT * FROM log');
    const listbox = page.getByRole('listbox', { name: /schema suggestions/i });
    await expect(listbox).toBeVisible();
    await expect(listbox.getByRole('option').first()).toContainText('logs');
    await editor.press('Tab');
    await expect(editor).toHaveValue('SELECT * FROM logs');
  });

  test('Open in SQL carries a translatable Explorer search across', async ({ page }) => {
    await page.goto('/explorer?q=' + encodeURIComponent('level:ERROR'));
    await page.getByTestId('open-in-sql').click();
    await expect(page).toHaveURL(/\/query\?sql=/);
    await expect(page.getByTestId('sql-editor')).toHaveValue(/FROM logs/);
    await expect(page.getByTestId('sql-editor')).toHaveValue(/level = 'ERROR'/);
  });
});
