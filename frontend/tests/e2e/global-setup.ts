import { test as setup } from '@playwright/test';
import fs from 'fs';
import path from 'path';

const GATEWAY = 'http://127.0.0.1:8080';
const AUTH_FILE = 'tests/e2e/.auth/state.json';

setup('authenticate', async ({ page, context }) => {
  const res = await fetch(`${GATEWAY}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: 'admin', password: 'admin' }),
  });
  const { token } = await res.json();
  if (!token) throw new Error('seed auth failed: no token returned');

  await context.addCookies([
    { name: 'pulse_token', value: token, domain: 'localhost', path: '/' },
  ]);

  await page.goto('/login');
  await page.evaluate((tok) => {
    localStorage.setItem('pulse_token', tok);
    localStorage.setItem('pulse_user', JSON.stringify({ id: 'u1', email: 'admin', role: 'admin' }));
  }, token);

  fs.mkdirSync(path.dirname(AUTH_FILE), { recursive: true });
  await context.storageState({ path: AUTH_FILE });
});
