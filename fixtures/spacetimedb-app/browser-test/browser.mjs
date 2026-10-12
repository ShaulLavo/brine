import { readFile, writeFile } from 'node:fs/promises';
import { chromium } from 'playwright';

const [appUrl, tokenFile, expectedBefore, outputFile] = process.argv.slice(2);
if (!appUrl || !tokenFile || !expectedBefore || !outputFile) throw new Error('browser proof requires app URL, private user token file, row count and output path');
const token = (await readFile(tokenFile, 'utf8')).trim();
const browser = await chromium.launch({ headless: true });
try {
  const page = await browser.newPage();
  page.setDefaultTimeout(10_000);
  let confirmedSocket = false;
  page.on('websocket', socket => {
    const url = new URL(socket.url());
    confirmedSocket ||= url.pathname.endsWith('/subscribe') && url.searchParams.get('confirmed') === 'true';
  });
  await page.goto(appUrl);
  await page.locator('#token').fill(token);
  await page.getByRole('button', { name: 'Connect', exact: true }).click();
  await page.waitForFunction(() => document.getElementById('status')?.textContent === 'Connected with confirmed reads', undefined, { timeout: 10_000 });
  await page.waitForFunction(count => document.getElementById('summary')?.textContent?.startsWith(`${count} confirmed rows`), expectedBefore, { timeout: 10_000 });
  await page.getByRole('button', { name: 'Write next marker', exact: true }).click();
  await page.waitForFunction(() => document.getElementById('status')?.textContent === 'Marker confirmed', undefined, { timeout: 10_000 });
  const evidence = JSON.parse(await page.locator('#evidence').innerText());
  if (!confirmedSocket || evidence.confirmedReads !== true || evidence.rows.length !== Number(expectedBefore) + 1) throw new Error('browser failed explicit confirmed subscription/write evidence');
  const inputCleared = (await page.locator('#token').inputValue()) === '';
  const noTokenPersistence = await page.evaluate(() => localStorage.length === 0 && sessionStorage.length === 0);
  if (!inputCleared || !noTokenPersistence) throw new Error('browser retained its input token');
  await writeFile(outputFile, JSON.stringify({ browserVersion: browser.version(), explicitConfirmedWebSocket: confirmedSocket, inputTokenCleared: inputCleared, noTokenPersistence, confirmedRows: evidence.rows.length }, null, 2) + '\n');
  await page.close();
} finally { await browser.close(); }
