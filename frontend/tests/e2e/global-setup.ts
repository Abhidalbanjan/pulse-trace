import { test as setup } from '@playwright/test';
import fs from 'fs';
import path from 'path';

const AUTH_FILE = 'tests/e2e/.auth/state.json';

// Authenticate through the real login form rather than injecting a token.
//
// The previous version called the gateway directly and pushed the token in with
// `context.addCookies()`. That silently did not work: Playwright's API reported
// the cookie as stored, but Chromium never persisted or sent it (verified — the
// first navigation request carried no Cookie header), so `storageState` was
// written with `cookies: []`. Since middleware.ts gates every protected route on
// the `pulse_token` cookie, all ~40 authenticated specs were redirected to
// /login and failed on "element not found". The setup itself still passed,
// because it asserted nothing.
//
// Driving the form makes AuthContext.login() set the cookie itself, which is
// both what a real user does and the only path that reliably persists.
setup('authenticate', async ({ page, context }) => {
  await page.goto('/login');

  await page.getByLabel('Email or Username').fill('admin');
  await page.getByLabel('Password', { exact: true }).fill('admin');
  await page.getByRole('button', { name: 'Sign In', exact: true }).click();

  // middleware.ts bounces an authenticated user off /login, so landing anywhere
  // else is the signal that the session took.
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), { timeout: 15_000 });

  fs.mkdirSync(path.dirname(AUTH_FILE), { recursive: true });
  const state = await context.storageState({ path: AUTH_FILE });

  // Fail loudly here rather than as ~40 confusing "element not found" failures
  // in every authenticated spec downstream.
  if (!state.cookies.some((c) => c.name === 'pulse_token')) {
    throw new Error(
      'auth setup failed: pulse_token cookie did not persist into storageState. ' +
        'middleware.ts gates every protected route on this cookie, so every ' +
        'authenticated spec would redirect to /login.',
    );
  }
});
