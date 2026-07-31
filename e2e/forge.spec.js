const { test, expect } = require('@playwright/test');

const demoPassword = 'forge-demo';

async function signIn(page, path = '/') {
  await page.goto(path);
  await page.getByLabel('Password').fill(demoPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
}

test('protected image controls stay hidden until sign-in', async ({ page }) => {
  await page.goto('/');

  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
  await expect(page.locator('#grantapp')).toBeHidden();
  await expect(page.getByLabel('Image')).toBeHidden();
  await expect(page.getByLabel('TTL (minutes)')).toBeHidden();

  await page.getByLabel('Password').fill(demoPassword);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();

  await expect(page.locator('#grantapp')).toBeVisible();
  await expect(page.getByText('Demo mode', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Image')).toHaveValue('demo/dev');
  await expect(page.getByLabel('TTL (minutes)')).toHaveValue('30');
});

test('component selection issues a fixture-backed grant', async ({ page }) => {
  await signIn(page);
  await page.getByLabel('Claude Code').uncheck();
  await page.getByLabel('Gemini CLI').check();

  await expect(page.locator('#vname')).toHaveText('codex-gemini');
  await page.getByRole('button', { name: 'Issue image path' }).click();

  await expect(page.locator('#out')).toBeVisible();
  await expect(page.locator('#vout')).toContainText('codex-gemini');
  await expect(page.locator('#exe')).toContainText('ssh exe.dev new --image=');
  await expect(page.locator('#raw')).toContainText('/t/');
  await expect(page.locator('#raw')).toContainText('/demo/dev:');
});

test('logout propagates to another open tab', async ({ page, context }) => {
  await signIn(page);
  const second = await context.newPage();
  await second.goto('/admin/');
  await expect(second.locator('#app')).toBeVisible();

  await page.getByRole('button', { name: 'Sign out' }).click();

  await expect(page.locator('#authgate')).toBeVisible();
  await expect(second.locator('#gate')).toBeVisible({ timeout: 3000 });
  await expect(second.locator('#app')).toBeHidden();
});

test('admin tabs expose fixture status without real credentials', async ({ page }) => {
  await signIn(page, '/admin/');

  await expect(page.locator('#app')).toBeVisible();
  await expect(page.locator('#metriccreds')).toHaveText('4/4');
  await expect(page.locator('#metricvariants')).toHaveText('16/16');

  await page.getByRole('tab', { name: 'CLI Logins' }).click();
  await expect(page.getByRole('heading', { name: 'Credential status' })).toBeVisible();
  await expect(page.getByRole('cell', { name: 'Codex CLI', exact: false })).toBeVisible();

  await page.getByRole('tab', { name: 'Images' }).click();
  await expect(page.getByRole('heading', { name: 'Bake credentialed images' })).toBeVisible();
  await expect(page.locator('#ctxbody')).toContainText('fixture data');

  await page.getByRole('tab', { name: 'Security' }).click();
  await expect(page.getByRole('heading', { name: 'Passkeys' })).toBeVisible();
});

test('documentation screenshots @screenshots', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'chromium', 'Documentation uses the desktop layout');

  await signIn(page);
  await expect(page.locator('#grantapp')).toBeVisible();
  await page.screenshot({
    path: 'docs/images/grant-page.png',
    fullPage: true,
  });

  await page.goto('/admin/');
  await expect(page.locator('#metriccreds')).toHaveText('4/4');
  await page.screenshot({
    path: 'docs/images/admin-overview.png',
    fullPage: true,
  });
});
