import { setGlobalLogLevel } from 'spacetimedb';
import { readFile } from 'node:fs/promises';
import { ConfirmedSession } from './session';

setGlobalLogLevel('error');
console.log = () => process.stderr.write('SpacetimeDB SDK diagnostic\n');

const [endpoint, database, action, sequenceText] = process.argv.slice(2);
if (!endpoint || !database || !action) throw new Error('usage: drill ENDPOINT DATABASE read|write|verify|denied [SEQUENCE]');
const tokenFile = process.env.BRINE_USER_TOKEN_FILE;
const token = tokenFile ? (await readFile(tokenFile, 'utf8')).trim() : undefined;
const connection = await ConfirmedSession.connect({ endpoint, database, token });
try {
  const current = connection.snapshot();
  if (action === 'read') process.stdout.write(`${JSON.stringify(current)}\n`);
  else if (action === 'write') {
    if (!sequenceText || !/^\d+$/.test(sequenceText)) throw new Error('sequence required');
    process.stdout.write(`${JSON.stringify(await connection.append(BigInt(sequenceText)))}\n`);
  } else if (action === 'verify') {
    if (!sequenceText || !current.rows.some(row => row.sequence === sequenceText)) throw new Error('expected recovery marker absent');
    process.stdout.write(`${JSON.stringify(current)}\n`);
  } else if (action === 'denied') {
    let refused = false;
    try { await connection.append(BigInt(current.rows.length + 1)); }
    catch (error) { refused = error instanceof Error && error.message.includes('authorized writer required'); }
    if (!refused) throw new Error('unauthorized write was not rejected');
    process.stdout.write('{"unauthorizedWrite":"rejected"}\n');
  } else throw new Error('unknown action');
} finally { connection.disconnect(); }
