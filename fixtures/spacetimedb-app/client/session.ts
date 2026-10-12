import { DbConnection } from './module_bindings';
import type { Identity } from 'spacetimedb';
import type { RecoveryLeft } from './module_bindings/types';

export type RecoveryRow = {
  sequence: string;
  marker: string;
  commitTimeMicros: string;
  scheduled: boolean;
};

export type RecoveryEvidence = {
  kind: 'confirmed_subscription';
  confirmedReads: true;
  schemaMarker: 'brine-recovery-v1';
  rows: RecoveryRow[];
};

function evidence(connection: DbConnection): RecoveryEvidence {
  const schema = Array.from(connection.db.schemaMarker.iter());
  if (schema.length !== 1 || schema[0]?.id !== 1 || schema[0]?.revision !== 'brine-recovery-v1') {
    throw new Error('fixed schema marker differs');
  }
  const left = Array.from(connection.db.recoveryLeft.iter(), normalize);
  if (left.length > 10_000) throw new Error('fixture row limit exceeded');
  const right = Array.from(connection.db.recoveryRight.iter(), normalize);
  left.sort(compareSequence);
  right.sort(compareSequence);
  if (JSON.stringify(left) !== JSON.stringify(right)) throw new Error('transaction-linked tables differ');
  left.forEach(validateRow);
  return { kind: 'confirmed_subscription', confirmedReads: true, schemaMarker: 'brine-recovery-v1', rows: left };
}

function normalize(row: RecoveryLeft): RecoveryRow {
  return { sequence: row.sequence.toString(), marker: row.marker, commitTimeMicros: row.commitTime.microsSinceUnixEpoch.toString(), scheduled: row.scheduled };
}

function compareSequence(a: RecoveryRow, b: RecoveryRow): number {
  return Number(BigInt(a.sequence) - BigInt(b.sequence));
}

function validateRow(row: RecoveryRow, index: number): void {
  if (BigInt(row.sequence) !== BigInt(index + 1)) throw new Error('sequence is not contiguous');
  if (row.marker !== `brine-p09-${row.sequence.padStart(8, '0')}`) throw new Error('marker differs');
  if (BigInt(row.commitTimeMicros) <= 0n) throw new Error('commit timestamp absent');
}

function subscribeConfirmed(connection: DbConnection, resolve: (connection: DbConnection) => void, reject: (error: unknown) => void, timer: ReturnType<typeof setTimeout>): void {
  connection.subscriptionBuilder().onApplied(() => { clearTimeout(timer); resolve(connection); })
    .onError(ctx => { clearTimeout(timer); connection.disconnect(); reject(ctx.event ?? new Error('subscription failed')); })
    .subscribe(['SELECT * FROM recovery_left', 'SELECT * FROM recovery_right', 'SELECT * FROM schema_marker']);
}

function openConnection(config: { endpoint: string; database: string; token?: string; onIdentity?: (identity: Identity) => void }): Promise<DbConnection> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => { connection.disconnect(); reject(new Error('connection/subscription deadline')); }, 10_000);
    const connection = DbConnection.builder().withUri(config.endpoint).withDatabaseName(config.database)
      .withToken(config.token).withConfirmedReads(true).withCompression('none')
      .onConnect((connected, identity) => {
        config.onIdentity?.(identity);
        subscribeConfirmed(connected, resolve, reject, timer);
      })
      .onConnectError((_ctx, error) => { clearTimeout(timer); reject(error); }).build();
  });
}

async function appendConfirmed(connection: DbConnection, sequence: bigint): Promise<RecoveryEvidence> {
  await connection.reducers.append({ sequence });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const result = evidence(connection);
    if (result.rows.some(row => BigInt(row.sequence) === sequence)) return result;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  throw new Error('acknowledged write lacks confirmed subscription marker');
}

export class ConfirmedSession {
  #connection: DbConnection;
  private constructor(connection: DbConnection) { this.#connection = connection; }
  static async connect(config: Parameters<typeof openConnection>[0]): Promise<ConfirmedSession> {
    return new ConfirmedSession(await openConnection(config));
  }
  snapshot(): RecoveryEvidence { return evidence(this.#connection); }
  append(sequence: bigint): Promise<RecoveryEvidence> { return appendConfirmed(this.#connection, sequence); }
  disconnect(): void { this.#connection.disconnect(); }
}
