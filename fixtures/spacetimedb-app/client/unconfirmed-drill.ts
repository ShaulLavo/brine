import { readFile } from 'node:fs/promises';
import { setGlobalLogLevel } from 'spacetimedb';
import { DbConnection } from './module_bindings';

setGlobalLogLevel('error');
console.log = () => process.stderr.write('SpacetimeDB SDK diagnostic\n');
const [endpoint, database, sequence] = process.argv.slice(2);
const tokenFile = process.env.BRINE_USER_TOKEN_FILE;
if (!endpoint || !database || !sequence || !tokenFile) throw new Error('bounded unconfirmed acknowledgement probe requires endpoint, database, sequence and token file');
const token = (await readFile(tokenFile, 'utf8')).trim();
const connection = await new Promise<DbConnection>((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('unconfirmed connection deadline')), 10_000);
  DbConnection.builder().withUri(endpoint).withDatabaseName(database).withToken(token).withConfirmedReads(false).withCompression('none')
    .onConnect(connection => { clearTimeout(timer); resolve(connection); })
    .onConnectError((_ctx, error) => { clearTimeout(timer); reject(error); }).build();
});
try {
  await connection.reducers.append({ sequence: BigInt(sequence) });
  process.stdout.write('{"kind":"reducer_acknowledgement","confirmedReads":false,"durabilityEvidence":false}\n');
} finally { connection.disconnect(); }
