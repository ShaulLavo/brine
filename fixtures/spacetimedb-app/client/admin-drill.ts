import { setGlobalLogLevel } from 'spacetimedb';
import { readFile } from 'node:fs/promises';
import { Identity } from 'spacetimedb';
import { DbConnection } from './module_bindings';

setGlobalLogLevel('error');
console.log = () => process.stderr.write('SpacetimeDB SDK diagnostic\n');

const [endpoint, database, action, value] = process.argv.slice(2);
const tokenFile = process.env.BRINE_PUBLISHER_TOKEN_FILE;
if (!endpoint || !database || !value || !tokenFile) throw new Error('usage: admin-drill ENDPOINT DATABASE authorize|schedule VALUE with private publisher token file');
const token = (await readFile(tokenFile, 'utf8')).trim();
const connection = await new Promise<DbConnection>((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('admin connection deadline')), 10_000);
  DbConnection.builder().withUri(endpoint).withDatabaseName(database).withToken(token).withConfirmedReads(true).withCompression('none')
    .onConnect(connection => { clearTimeout(timer); resolve(connection); })
    .onConnectError((_ctx, error) => { clearTimeout(timer); reject(error); }).build();
});
try {
  if (action === 'authorize') await connection.reducers.authorizeWriter({ identity: Identity.fromString(value) });
  else if (action === 'schedule' && /^\d+$/.test(value)) await connection.reducers.scheduleWrites({ count: Number(value) });
  else throw new Error('unknown admin action or invalid value');
  process.stdout.write('{"reducer":"acknowledged","durabilityEvidence":false}\n');
} finally { connection.disconnect(); }
