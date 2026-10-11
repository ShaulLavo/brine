import { ConfirmedSession } from './session';

function field(id: string): HTMLInputElement {
  const value = document.getElementById(id);
  if (!(value instanceof HTMLInputElement)) throw new Error(`input ${id} absent`);
  return value;
}
function element(id: string): HTMLElement {
  const value = document.getElementById(id);
  if (!value) throw new Error(`element ${id} absent`);
  return value;
}
const writeButton = document.getElementById('write');
if (!(writeButton instanceof HTMLButtonElement)) throw new Error('write button absent');
let session: ConfirmedSession | undefined;
let refresh: ReturnType<typeof setInterval> | undefined;
function render(): void {
  if (!session) return;
  const result = session.snapshot();
  element('summary').textContent = `${result.rows.length} confirmed rows, schema ${result.schemaMarker}`;
  element('evidence').textContent = JSON.stringify(result, null, 2);
  element('rows').replaceChildren(...result.rows.reverse().slice(0, 20).map(row => {
    const tr = document.createElement('tr');
    for (const value of [row.sequence, row.marker, row.commitTimeMicros, String(row.scheduled)]) {
      const td = document.createElement('td'); td.textContent = value; tr.append(td);
    }
    return tr;
  }));
}
function refreshEvidence(): void { try { render(); } catch (error) { failure(error); } }
function failure(error: unknown): void { element('status').textContent = error instanceof Error ? error.message : 'request failed'; }
element('connect').addEventListener('submit', async event => {
  event.preventDefault();
  session?.disconnect();
  if (refresh) clearInterval(refresh);
  writeButton.disabled = true;
  element('status').textContent = 'Connecting';
  try {
    session = await ConfirmedSession.connect({ endpoint: field('endpoint').value, database: field('database').value, token: field('token').value || undefined });
    field('token').value = '';
    render();
    refresh = setInterval(refreshEvidence, 500);
    writeButton.disabled = false;
    element('status').textContent = 'Connected with confirmed reads';
  } catch (error) { failure(error); }
});
writeButton.addEventListener('click', async () => {
  if (!session) return;
  writeButton.disabled = true;
  try { await session.append(BigInt(session.snapshot().rows.length + 1)); render(); element('status').textContent = 'Marker confirmed'; }
  catch (error) { failure(error); }
  finally { writeButton.disabled = false; }
});
const config: unknown = await (await fetch('/config.json')).json();
if (typeof config !== 'object' || config === null || !('endpoint' in config) || !('database' in config)) throw new Error('public config malformed');
if (typeof config.endpoint !== 'string' || typeof config.database !== 'string') throw new Error('public config values malformed');
field('endpoint').value = config.endpoint;
field('database').value = config.database;
