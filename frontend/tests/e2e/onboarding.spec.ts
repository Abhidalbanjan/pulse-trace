import { test, expect } from '@playwright/test';

test.describe('Onboarding wizard', () => {
  test('walks through all 3 steps and generates an install script', async ({ page }) => {
    await page.goto('/onboarding');
    await expect(page.getByText('Welcome to PulseTrace.')).toBeVisible();

    await page.getByText('Kubernetes', { exact: true }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await expect(page.getByText('Generate Ingestion Key')).toBeVisible();
    await page.getByRole('button', { name: 'Generate API Key' }).click();

    await expect(page.getByText("You're all set!")).toBeVisible({ timeout: 10000 });
    // Real OpenTelemetry setup, not a fictional vendor agent: the Collector helm
    // install, the standard OTLP exporter env, and a runnable curl test event.
    await expect(page.locator('pre', { hasText: 'helm install otel-collector' })).toBeVisible();
    await expect(page.locator('pre', { hasText: 'OTEL_EXPORTER_OTLP_ENDPOINT' })).toBeVisible();
    await expect(page.locator('pre', { hasText: 'curl -X POST' })).toBeVisible();
  });

  test('offers real OTel snippets across languages (Node.js)', async ({ page }) => {
    await page.goto('/onboarding');
    await page.getByText('Node.js', { exact: true }).click();
    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByRole('button', { name: 'Generate API Key' }).click();
    await expect(page.getByText("You're all set!")).toBeVisible({ timeout: 10000 });
    // The Node path uses the OTel auto-instrumentation register hook + a Bearer
    // header carrying the minted key.
    await expect(page.locator('pre', { hasText: 'auto-instrumentations-node' })).toBeVisible();
    await expect(page.locator('pre', { hasText: 'Authorization=Bearer' })).toBeVisible();
  });
});
