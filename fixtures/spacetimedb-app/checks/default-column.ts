import { readFile } from 'node:fs/promises';
import { setGlobalLogLevel } from 'spacetimedb';
import { DbConnection } from './module_bindings';

setGlobalLogLevel('error');
console.log = () => process.stderr.write('SpacetimeDB SDK diagnostic\n');
const [endpoint, database, expectedRows] = process.argv.slice(2);
const tokenFile = process.env.BRINE_USER_TOKEN_FILE;
if (!endpoint || !database || !expectedRows || !tokenFile) throw new Error('default-column check requires endpoint/database/rows/private user token file');
const token = (await readFile(tokenFile, 'utf8')).trim();
const connection = await new Promise<DbConnection>((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('default client deadline')), 10_000);
  DbConnection.builder().withUri(endpoint).withDatabaseName(database).withToken(token).withConfirmedReads(true).withCompression('none')
    .onConnect(connection => subscribe(connection, resolve, reject, timer))
    .onConnectError((_ctx, error) => { clearTimeout(timer); reject(error); }).build();
});
function subscribe(connection: DbConnection, resolve: (connection: DbConnection) => void, reject: (error: unknown) => void, timer: ReturnType<typeof setTimeout>): void {
  connection.subscriptionBuilder().onApplied(() => { clearTimeout(timer); resolve(connection); })
    .onError(ctx => { clearTimeout(timer); reject(ctx.event); })
    .subscribe(['SELECT * FROM recovery_left', 'SELECT * FROM recovery_right', 'SELECT * FROM schema_marker']);
}
try {
  const left = Array.from(connection.db.recoveryLeft.iter());
  const right = Array.from(connection.db.recoveryRight.iter());
  if (left.length !== Number(expectedRows) || right.length !== left.length) throw new Error('default client row count differs');
  if (!left.every(row => row.rating === 7) || !right.every(row => row.rating === 7)) throw new Error('appended defaults are not 7');
  const pairs = left.every(row => {
    const paired = connection.db.recoveryRight.sequence.find(row.sequence);
    return paired?.marker === row.marker && paired.commitTime.microsSinceUnixEpoch === row.commitTime.microsSinceUnixEpoch && paired.rating === row.rating;
  });
  if (!pairs) throw new Error('default client transaction pairs differ');
  process.stdout.write(JSON.stringify({ explicitConfirmedReads: true, defaultColumn: 'rating', defaultValue: 7, matchedClientRows: left.length }) + '\n');
} finally { connection.disconnect(); }
