use crate::framing::Head;
use crate::protocol::{
    Deadline, History, Limits, Refusal, Result, SnapshotEvidence, TableEvidence, add,
};
use sha2::{Digest, Sha256};
use spacetimedb_datastore::{
    execution_context::Workload,
    system_tables::{ModuleKind, ST_MODULE_ID, ST_TABLE_ID},
};
use spacetimedb_durability::{Close, Durability, DurableOffset, PreparedTx, local::LocalHistory};
use spacetimedb_engine::{
    persistence::Persistence,
    relational_db::{RelationalDB, Txdata},
    snapshot::{Compression, SnapshotWorker},
};
use spacetimedb_lib::Identity;
use spacetimedb_paths::server::ReplicaDir;
use spacetimedb_runtime::Handle;
use spacetimedb_sats::{bsatn, buffer::BufWriter};
use spacetimedb_snapshot::{Snapshot, SnapshotRepository};
use spacetimedb_table::page_pool::PagePool;
use std::sync::Arc;

struct Frozen {
    sender: tokio::sync::watch::Sender<Option<u64>>,
}

impl Durability for Frozen {
    type TxData = Txdata;
    fn append_tx(&self, _: PreparedTx<Txdata>) {
        panic!("offline write refused");
    }
    fn durable_tx_offset(&self) -> DurableOffset {
        self.sender.subscribe().into()
    }
    fn close(&self) -> Close {
        let offset = *self.sender.borrow();
        Box::pin(async move { offset })
    }
}

pub struct State {
    pub database: String,
    pub owner: String,
    pub program_kind: &'static str,
    pub program_digest: String,
    pub program_wire: String,
    pub program_sha256: String,
    pub program_bytes: u64,
    pub connected: u64,
    pub tables: Vec<TableEvidence>,
    pub hash: String,
    pub snapshot: Option<SnapshotEvidence>,
}

pub fn observe(
    root: &ReplicaDir,
    database: Identity,
    owner: Identity,
    history: History,
    head: &Head,
    limits: &Limits,
    deadline: &Deadline,
) -> Result<State> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|_| Refusal::Native)?;
    let (worker, snapshot) = snapshot(root, database, history, head, &runtime)?;
    let (sender, _) = tokio::sync::watch::channel(Some(head.last));
    let persistence = Persistence::new(
        Frozen { sender },
        || Ok(Default::default()),
        worker,
        Handle::tokio(runtime.handle().clone()),
    );
    let history = LocalHistory::open(root.commit_log()).map_err(|_| Refusal::Framing)?;
    let (db, clients) = RelationalDB::open(
        database,
        owner,
        history,
        Some(persistence),
        None,
        PagePool::new(Some(8 << 20)),
    )
    .map_err(|_| Refusal::Identity)?;
    deadline.check()?;
    check_counts(&db, limits, deadline)?;
    let metadata = db
        .metadata()
        .map_err(|_| Refusal::Native)?
        .ok_or(Refusal::MissingHistory)?;
    if metadata.database_identity != database || metadata.owner_identity != owner {
        return Err(Refusal::Identity);
    }
    let (program, offset) = db.program_with_tx_offset().map_err(|_| Refusal::Native)?;
    let program = program.ok_or(Refusal::MissingHistory)?;
    if offset != head.last || program.bytes.len() as u64 > limits.program_bytes {
        return Err(Refusal::Program);
    }
    if spacetimedb_lib::hash_bytes(&program.bytes) != program.hash
        || metadata.program_hash != program.hash
    {
        return Err(Refusal::Program);
    }
    let kind = match program.kind {
        ModuleKind::WASM => "wasm",
        ModuleKind::JS => "js",
        _ => return Err(Refusal::Program),
    };
    let tables = tables(&db, limits, deadline)?;
    let mut hash = blake3::Hasher::new();
    hash.update(b"brine-native-state-v1\0");
    hash.update(&metadata.database_identity.to_byte_array());
    hash.update(&metadata.owner_identity.to_byte_array());
    hash.update(&program.hash.data);
    hash.update(&offset.to_le_bytes());
    hash.update(&(tables.len() as u64).to_le_bytes());
    for table in &tables {
        hash.update(table.multiset_blake3.as_bytes());
    }
    Ok(State {
        database: metadata.database_identity.to_string(),
        owner: metadata.owner_identity.to_string(),
        program_kind: kind,
        program_digest: program.hash.to_string(),
        program_wire: hex(program.hash.data.into_iter().rev()),
        program_sha256: hex(Sha256::digest(&program.bytes)),
        program_bytes: program.bytes.len() as u64,
        connected: clients.len() as u64,
        tables,
        hash: hash.finalize().to_hex().to_string(),
        snapshot,
    })
}

fn check_counts(db: &RelationalDB, limits: &Limits, deadline: &Deadline) -> Result<()> {
    let tx = db.begin_tx(Workload::Internal);
    let mut tables = 0;
    for _ in db.iter(&tx, ST_TABLE_ID).map_err(|_| Refusal::Native)? {
        deadline.check()?;
        add(&mut tables, 1, limits.tables, Refusal::StateLimit)?;
    }
    let mut modules = 0;
    for row in db.iter(&tx, ST_MODULE_ID).map_err(|_| Refusal::Native)? {
        add(&mut modules, 1, 1, Refusal::Program)?;
        if bsatn::to_len(&row).map_err(|_| Refusal::Program)? as u64 > limits.program_bytes {
            return Err(Refusal::Program);
        }
    }
    let _ = db.release_tx(tx);
    if modules != 1 {
        return Err(Refusal::MissingHistory);
    }
    Ok(())
}

fn snapshot(
    root: &ReplicaDir,
    database: Identity,
    history: History,
    head: &Head,
    runtime: &tokio::runtime::Runtime,
) -> Result<(Option<SnapshotWorker>, Option<SnapshotEvidence>)> {
    let History::Snapshot { offset, replica_id } = history else {
        if head.first != 0 {
            return Err(Refusal::Snapshot);
        }
        return Ok((None, None));
    };
    if replica_id == 0 || offset > head.last || (head.first > 0 && offset < head.first - 1) {
        return Err(Refusal::Snapshot);
    }
    let path = root.snapshots().snapshot_dir(offset).snapshot_file(offset);
    let (snapshot, _) = Snapshot::read_from_file(&path).map_err(|_| Refusal::Snapshot)?;
    if snapshot.database_identity != database
        || snapshot.replica_id != replica_id
        || snapshot.tx_offset != offset
    {
        return Err(Refusal::Snapshot);
    }
    let repo = Arc::new(
        SnapshotRepository::open(root.snapshots(), database, replica_id)
            .map_err(|_| Refusal::Snapshot)?,
    );
    let offsets = repo
        .all_snapshots()
        .map_err(|_| Refusal::Snapshot)?
        .collect::<Vec<_>>();
    if offsets != [offset] {
        return Err(Refusal::Snapshot);
    }
    let evidence = SnapshotEvidence {
        observed_database_identity: snapshot.database_identity.to_string(),
        observed_replica_id: snapshot.replica_id,
        offset: snapshot.tx_offset,
    };
    let worker = SnapshotWorker::new(
        repo,
        Compression::Disabled,
        Handle::tokio(runtime.handle().clone()),
    );
    Ok((Some(worker), Some(evidence)))
}

struct HashWriter {
    hash: blake3::Hasher,
}

impl BufWriter for HashWriter {
    fn put_slice(&mut self, bytes: &[u8]) {
        self.hash.update(bytes);
    }
}

fn tables(db: &RelationalDB, limits: &Limits, deadline: &Deadline) -> Result<Vec<TableEvidence>> {
    let tx = db.begin_tx(Workload::Internal);
    let mut schemas = db.get_all_tables(&tx).map_err(|_| Refusal::Native)?;
    schemas.sort_unstable_by_key(|schema| schema.table_id);
    let mut output = Vec::new();
    let mut count = 0;
    let mut bytes = 0;
    for schema in schemas {
        deadline.check()?;
        if schema.table_name.len() > 1024 {
            return Err(Refusal::StateLimit);
        }
        let rows = db.iter(&tx, schema.table_id).map_err(|_| Refusal::Native)?;
        let (digests, table_bytes) = row_digests(rows, &mut count, &mut bytes, limits, deadline)?;
        let mut hash = blake3::Hasher::new();
        hash.update(b"brine-native-table-v1\0");
        hash.update(&schema.table_id.0.to_le_bytes());
        hash.update(&(schema.table_name.len() as u64).to_le_bytes());
        hash.update(schema.table_name.as_bytes());
        hash.update(&(digests.len() as u64).to_le_bytes());
        hash.update(&table_bytes.to_le_bytes());
        for digest in &digests {
            hash.update(digest);
        }
        output.push(TableEvidence {
            table_id: schema.table_id.0,
            name: schema.table_name.to_string(),
            rows: digests.len() as u64,
            bsatn_bytes: table_bytes,
            multiset_blake3: hash.finalize().to_hex().to_string(),
        });
    }
    let _ = db.release_tx(tx);
    Ok(output)
}

fn row_digests<'a>(
    rows: impl Iterator<Item = spacetimedb_table::table::RowRef<'a>>,
    count: &mut u64,
    bytes: &mut u64,
    limits: &Limits,
    deadline: &Deadline,
) -> Result<(Vec<[u8; 32]>, u64)> {
    let mut digests = Vec::new();
    let mut table_bytes = 0;
    for row in rows {
        deadline.check()?;
        add(count, 1, limits.rows, Refusal::StateLimit)?;
        let size = bsatn::to_len(&row).map_err(|_| Refusal::Native)? as u64;
        if size > limits.row_bytes {
            return Err(Refusal::StateLimit);
        }
        add(bytes, size, limits.state_bytes, Refusal::StateLimit)?;
        add(
            &mut table_bytes,
            size,
            limits.state_bytes,
            Refusal::StateLimit,
        )?;
        let mut writer = HashWriter {
            hash: blake3::Hasher::new(),
        };
        writer.hash.update(b"brine-native-row-v1\0");
        writer.hash.update(&size.to_le_bytes());
        bsatn::to_writer(&mut writer, &row).map_err(|_| Refusal::Native)?;
        digests.push(*writer.hash.finalize().as_bytes());
    }
    digests.sort_unstable();
    Ok((digests, table_bytes))
}

fn hex(bytes: impl IntoIterator<Item = u8>) -> String {
    bytes
        .into_iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}
