use super::*;
use protocol::{History, Limits};
use spacetimedb_commitlog::repo::Repo;
use spacetimedb_datastore::{
    execution_context::Workload,
    system_tables::ModuleKind,
    traits::{IsolationLevel, Program},
};
use spacetimedb_durability::Durability;
use spacetimedb_engine::{
    persistence::Persistence,
    relational_db::{RelationalDB, local_durability_with_options, open_snapshot_repo},
    snapshot::{Compression, SnapshotWorker},
};
use spacetimedb_lib::bsatn::ToBsatn;
use spacetimedb_runtime::Handle;
use spacetimedb_sats::{AlgebraicType, ProductType, product};
use spacetimedb_table::page_pool::PagePool;
use std::fs;
use std::os::unix::fs::{PermissionsExt, symlink};

struct Fixture {
    _directory: tempfile::TempDir,
    root: std::path::PathBuf,
    request: Request,
    selected_snapshot: Option<u64>,
}

fn id(byte: u8) -> Identity {
    Identity::from_byte_array([byte; 32])
}

fn seed(snapshot: bool) -> Fixture {
    let directory = tempfile::tempdir().unwrap();
    let root = directory.path().join("replica");
    fs::create_dir(&root).unwrap();
    fs::set_permissions(&root, fs::Permissions::from_mode(0o700)).unwrap();
    let replica = ReplicaDir::from_path_unchecked(root.clone());
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    let worker = snapshot.then(|| {
        let repo = open_snapshot_repo(replica.snapshots(), id(1), 1).unwrap();
        SnapshotWorker::new(
            repo,
            Compression::Disabled,
            Handle::tokio(runtime.handle().clone()),
        )
    });
    let mut options = spacetimedb_durability::local::Options::default();
    options.commitlog.max_segment_size = 4096;
    options.commitlog.preallocate_segments = false;
    let (local, disk) = runtime
        .block_on(local_durability_with_options(
            replica,
            Handle::tokio(runtime.handle().clone()),
            None,
            options,
        ))
        .unwrap();
    let persistence = Persistence {
        durability: local.clone(),
        disk_size: disk,
        snapshots: worker.clone(),
        runtime: Handle::tokio(runtime.handle().clone()),
    };
    let (db, _) = RelationalDB::open(
        id(1),
        id(2),
        local.as_history(),
        Some(persistence),
        None,
        PagePool::new(Some(8 << 20)),
    )
    .unwrap();
    let mut tx = db.begin_mut_tx(IsolationLevel::Serializable, Workload::Internal);
    db.set_initialized(
        &mut tx,
        Program::from_bytes(ModuleKind::WASM, b"initial-program".to_vec()),
    )
    .unwrap();
    let mut schema =
        spacetimedb_schema::schema::TableSchema::from_product_type(ProductType::from([
            (Some("seq"), AlgebraicType::U64),
            (Some("payload"), AlgebraicType::String),
        ]));
    schema.table_name = spacetimedb_schema::table_name::TableName::for_test("reader_fixture");
    let table = db.create_table(&mut tx, schema).unwrap();
    db.commit_tx(tx).unwrap();
    for seq in 0..20u64 {
        let mut tx = db.begin_mut_tx(IsolationLevel::Serializable, Workload::Internal);
        db.insert(
            &mut tx,
            table,
            &product![seq, "x".repeat(256)].to_bsatn_vec().unwrap(),
        )
        .unwrap();
        db.commit_tx(tx).unwrap();
    }
    let mut tx = db.begin_mut_tx(IsolationLevel::Serializable, Workload::Internal);
    db.update_program(
        &mut tx,
        Program::from_bytes(ModuleKind::WASM, b"current-program".to_vec()),
    )
    .unwrap();
    db.commit_tx(tx).unwrap();
    let (_, offset) = db.program_with_tx_offset().unwrap();
    runtime
        .block_on(local.durable_tx_offset().wait_for(offset))
        .unwrap();
    if let Some(worker) = worker {
        let mut receiver = worker.subscribe();
        worker.request_snapshot();
        runtime
            .block_on(async {
                tokio::time::timeout(
                    std::time::Duration::from_secs(10),
                    receiver.wait_for(|value| value.is_some_and(|current| current >= offset)),
                )
                .await
            })
            .unwrap()
            .unwrap();
    }
    assert_eq!(runtime.block_on(db.shutdown()), Some(offset));
    Fixture {
        _directory: directory,
        root,
        selected_snapshot: snapshot.then_some(offset),
        request: Request {
            format: FORMAT.into(),
            expected_database_identity: id(1).to_string(),
            expected_owner_identity: id(2).to_string(),
            history: if snapshot {
                History::Snapshot {
                    offset,
                    replica_id: 1,
                }
            } else {
                History::Logs {}
            },
            limits: Limits::MAX,
        },
    }
}

fn copy_tree(source: &Path, target: &Path) {
    fs::create_dir(target).unwrap();
    fs::set_permissions(target, fs::Permissions::from_mode(0o700)).unwrap();
    for entry in fs::read_dir(source).unwrap() {
        let entry = entry.unwrap();
        let destination = target.join(entry.file_name());
        if entry.file_type().unwrap().is_dir() {
            copy_tree(&entry.path(), &destination);
            continue;
        }
        fs::copy(entry.path(), destination).unwrap();
    }
}

fn observed(fixture: &Fixture, alter: impl FnOnce(&Path)) -> Result<Evidence> {
    let before = input::files(&fixture.root, &Limits::MAX, &Deadline::new(60_000)).unwrap();
    let copy = tempfile::tempdir().unwrap();
    let root = copy.path().join("replica");
    copy_tree(&fixture.root, &root);
    select_snapshot_copy(&root, fixture.selected_snapshot);
    alter(&root);
    let result = inspect(
        &root,
        &fixture.request,
        identity(&fixture.request.expected_database_identity)?,
        identity(&fixture.request.expected_owner_identity)?,
        &Deadline::new(fixture.request.limits.deadline_ms),
    );
    assert!(before == input::files(&fixture.root, &Limits::MAX, &Deadline::new(60_000)).unwrap());
    result
}

fn select_snapshot_copy(root: &Path, selected: Option<u64>) {
    let Some(offset) = selected else { return };
    let expected = format!("{offset:020}.snapshot_dir");
    for entry in fs::read_dir(root.join("snapshots")).unwrap() {
        let entry = entry.unwrap();
        if entry.file_name() == expected.as_str() {
            continue;
        }
        fs::remove_dir_all(entry.path()).unwrap();
    }
}

#[test]
fn current_program_and_all_state_match_logs_snapshot_and_compression() {
    let mut fixture = seed(true);
    let baseline = observed(&fixture, |_| {}).unwrap();
    assert_eq!(baseline.observed_database_identity, id(1).to_string());
    assert_eq!(baseline.observed_owner_identity, id(2).to_string());
    assert_eq!(
        baseline.current_program_digest,
        spacetimedb_lib::hash_bytes(b"current-program").to_string()
    );
    assert_ne!(
        baseline.current_program_digest,
        spacetimedb_lib::hash_bytes(b"initial-program").to_string()
    );
    assert_eq!(baseline.current_program_sha256, hex_sha(b"current-program"));
    assert_eq!(
        baseline
            .tables
            .iter()
            .find(|table| table.name == "reader_fixture")
            .unwrap()
            .rows,
        20
    );
    fixture.request.history = History::Logs {};
    assert_eq!(
        observed(&fixture, |_| {}).unwrap().state_blake3,
        baseline.state_blake3
    );
    let compressed = observed(&fixture, |root| {
        let repo = spacetimedb_commitlog::repo::Fs::new(
            ReplicaDir::from_path_unchecked(root.to_path_buf()).commit_log(),
            None,
        )
        .unwrap();
        let mut offsets = repo.existing_offsets().unwrap();
        offsets.pop();
        for offset in offsets {
            repo.compress_segment(offset).unwrap();
        }
    })
    .unwrap();
    assert_eq!(compressed.state_blake3, baseline.state_blake3);
}

fn hex_sha(bytes: &[u8]) -> String {
    use sha2::Digest;
    sha2::Sha256::digest(bytes)
        .into_iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

#[test]
fn wrong_expected_identities_never_become_observed_authority() {
    let mut fixture = seed(false);
    fixture.request.expected_database_identity = id(9).to_string();
    assert!(matches!(observed(&fixture, |_| {}), Err(Refusal::Identity)));
    fixture.request.expected_database_identity = id(1).to_string();
    fixture.request.expected_owner_identity = id(9).to_string();
    assert!(matches!(observed(&fixture, |_| {}), Err(Refusal::Identity)));
}

fn last_log(root: &Path) -> std::path::PathBuf {
    let mut files = fs::read_dir(root.join("clog"))
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .filter(|path| path.extension().is_some_and(|extension| extension == "log"))
        .collect::<Vec<_>>();
    files.sort_unstable();
    files.pop().unwrap()
}

#[test]
fn torn_headers_payloads_oversized_lengths_and_missing_history_refuse() {
    let fixture = seed(false);
    assert!(matches!(
        observed(&fixture, |root| {
            let path = last_log(root);
            let mut bytes = fs::read(&path).unwrap();
            bytes.push(1);
            fs::write(path, bytes).unwrap();
        }),
        Err(Refusal::Framing)
    ));
    assert!(matches!(
        observed(&fixture, |root| {
            let path = last_log(root);
            let mut bytes = fs::read(&path).unwrap();
            bytes.pop();
            fs::write(path, bytes).unwrap();
        }),
        Err(Refusal::Framing)
    ));
    assert!(matches!(
        observed(&fixture, |root| {
            let path = last_log(root);
            let mut bytes = fs::read(&path).unwrap();
            bytes[28..32].copy_from_slice(&u32::MAX.to_le_bytes());
            fs::write(path, bytes).unwrap();
        }),
        Err(Refusal::Framing)
    ));
    assert!(matches!(
        observed(&fixture, |root| {
            fs::remove_dir_all(root.join("clog")).unwrap();
            fs::create_dir(root.join("clog")).unwrap();
        }),
        Err(Refusal::MissingHistory)
    ));
}

#[test]
fn logical_input_row_table_and_program_limits_refuse() {
    let mut fixture = seed(false);
    fixture.request.limits.files = 1;
    assert!(observed(&fixture, |_| {}).is_err());
    fixture.request.limits = Limits::MAX;
    fixture.request.limits.stored_bytes = 1;
    assert!(matches!(
        observed(&fixture, |_| {}),
        Err(Refusal::InputLimit)
    ));
    fixture.request.limits = Limits::MAX;
    fixture.request.limits.expanded_bytes = 1;
    assert!(matches!(
        observed(&fixture, |_| {}),
        Err(Refusal::ExpansionLimit)
    ));
    fixture.request.limits = Limits::MAX;
    fixture.request.limits.rows = 1;
    assert!(matches!(
        observed(&fixture, |_| {}),
        Err(Refusal::StateLimit)
    ));
    fixture.request.limits = Limits::MAX;
    fixture.request.limits.tables = 1;
    assert!(matches!(
        observed(&fixture, |_| {}),
        Err(Refusal::StateLimit)
    ));
    fixture.request.limits = Limits::MAX;
    fixture.request.limits.row_bytes = 1;
    assert!(matches!(
        observed(&fixture, |_| {}),
        Err(Refusal::StateLimit)
    ));
    fixture.request.limits = Limits::MAX;
    fixture.request.limits.program_bytes = 1;
    assert!(matches!(observed(&fixture, |_| {}), Err(Refusal::Program)));
}

#[test]
fn symlinks_and_expired_deadlines_refuse_without_source_effect() {
    let fixture = seed(false);
    assert!(matches!(
        observed(&fixture, |root| {
            symlink("/etc/passwd", root.join("clog/foreign")).unwrap();
        }),
        Err(Refusal::UnsafeMember)
    ));
    assert!(matches!(Deadline::new(0).check(), Err(Refusal::Deadline)));
    let mut output = BoundedOutput {
        bytes: Vec::new(),
        maximum: 3,
    };
    assert!(output.write_all(b"four").is_err());
    assert!(output.bytes.is_empty());
}

#[test]
fn emit_cli_fixture_when_requested() {
    let Some(output) = std::env::var_os("BRINE_READER_TEST_OUTPUT") else {
        return;
    };
    let output = std::path::PathBuf::from(output);
    fs::create_dir(&output).unwrap();
    let fixture = seed(true);
    copy_tree(&fixture.root, &output.join("replica"));
    select_snapshot_copy(&output.join("replica"), fixture.selected_snapshot);
    fs::write(
        output.join("request.json"),
        serde_json::to_vec(&fixture.request).unwrap(),
    )
    .unwrap();
}

#[test]
fn snapshot_suffix_requires_connected_snapshot_and_actual_replica_identity() {
    let mut fixture = seed(true);
    let baseline = observed(&fixture, |_| {}).unwrap();
    let remove_older = |root: &Path| {
        let last = last_log(root);
        for entry in fs::read_dir(root.join("clog")).unwrap() {
            let path = entry.unwrap().path();
            if path.extension().is_some_and(|extension| extension == "log") && path != last {
                fs::remove_file(&path).unwrap();
                fs::remove_file(path.with_extension("ofs")).unwrap();
            }
        }
    };
    assert_eq!(
        observed(&fixture, remove_older).unwrap().state_blake3,
        baseline.state_blake3
    );
    let History::Snapshot { offset, .. } = fixture.request.history else {
        panic!()
    };
    fixture.request.history = History::Snapshot {
        offset,
        replica_id: 2,
    };
    assert!(matches!(observed(&fixture, |_| {}), Err(Refusal::Snapshot)));
    fixture.request.history = History::Logs {};
    assert!(matches!(
        observed(&fixture, remove_older),
        Err(Refusal::Snapshot)
    ));
}

#[test]
fn cleanly_removed_complete_commit_requires_independent_source_witness() {
    let fixture = seed(false);
    let baseline = observed(&fixture, |_| {}).unwrap();
    let older = observed(&fixture, |root| {
        let path = last_log(root);
        let bytes = fs::read(&path).unwrap();
        let mut pos = 10;
        let mut last = 0;
        while pos < bytes.len() {
            last = pos;
            let length = u32::from_le_bytes(bytes[pos + 18..pos + 22].try_into().unwrap()) as usize;
            pos += 22 + length + 4;
        }
        assert_eq!(pos, bytes.len());
        fs::write(path, &bytes[..last]).unwrap();
    })
    .unwrap();
    assert!(older.committed_offset < baseline.committed_offset);
    assert_eq!(
        older.current_program_digest,
        spacetimedb_lib::hash_bytes(b"initial-program").to_string()
    );
    assert_ne!(older.state_blake3, baseline.state_blake3);
}

#[test]
fn selected_snapshot_objects_must_be_complete_without_log_fallback() {
    let fixture = seed(true);
    let History::Snapshot { offset, .. } = fixture.request.history else {
        panic!()
    };
    assert!(matches!(
        observed(&fixture, |root| {
            let objects = root
                .join("snapshots")
                .join(format!("{offset:020}.snapshot_dir"))
                .join("objects");
            let prefix = fs::read_dir(objects)
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path();
            let object = fs::read_dir(prefix)
                .unwrap()
                .next()
                .unwrap()
                .unwrap()
                .path();
            fs::remove_file(object).unwrap();
        }),
        Err(Refusal::Snapshot)
    ));
}

#[test]
fn logs_history_is_an_exact_empty_object() {
    let fixture = seed(false);
    let mut request = serde_json::to_value(&fixture.request).unwrap();
    for history in [
        serde_json::json!({"kind":"logs","x":1}),
        serde_json::json!({"kind":"logs","offset":21,"replica_id":1}),
    ] {
        request["history"] = history;
        assert!(serde_json::from_value::<Request>(request.clone()).is_err());
    }
}
#[test]
fn native_selected_snapshot_with_empty_required_log_suffix() {
    let fixture = seed(true);
    let baseline = observed(&fixture, |_| {}).unwrap();
    let copy = tempfile::tempdir().unwrap();
    let root = copy.path().join("replica");
    copy_tree(&fixture.root, &root);
    select_snapshot_copy(&root, fixture.selected_snapshot);
    fs::remove_dir_all(root.join("clog")).unwrap();
    fs::create_dir(root.join("clog")).unwrap();
    let History::Snapshot { offset, replica_id } = fixture.request.history else {
        panic!()
    };
    let replica = ReplicaDir::from_path_unchecked(root.clone());
    let repository =
        spacetimedb_snapshot::SnapshotRepository::open(replica.snapshots(), id(1), replica_id)
            .unwrap();
    let actual = repository
        .read_snapshot(offset, &PagePool::new(Some(8 << 20)))
        .unwrap();
    let actual_head = framing::Head {
        first: 0,
        last: actual.tx_offset,
        commits: 0,
    };
    assert_eq!(actual_head.last, baseline.committed_offset);
    let before = input::files(&root, &Limits::MAX, &Deadline::new(60_000)).unwrap();
    let state = state::observe(
        &replica,
        id(1),
        id(2),
        fixture.request.history,
        Some(&actual_head),
        &Limits::MAX,
        &Deadline::new(60_000),
    )
    .unwrap();
    assert_eq!(state.database, baseline.observed_database_identity);
    assert_eq!(state.owner, baseline.observed_owner_identity);
    assert_eq!(state.program_digest, baseline.current_program_digest);
    assert_eq!(state.program_wire, baseline.current_program_hash);
    assert_eq!(state.program_sha256, baseline.current_program_sha256);
    assert_eq!(state.hash, baseline.state_blake3);
    assert_eq!(
        serde_json::to_vec(&state.tables).unwrap(),
        serde_json::to_vec(&baseline.tables).unwrap()
    );
    assert_eq!(state.snapshot.unwrap().offset, actual_head.last);
    assert!(before == input::files(&root, &Limits::MAX, &Deadline::new(60_000)).unwrap());
}

fn empty_logs(root: &Path) {
    fs::remove_dir_all(root.join("clog")).unwrap();
    fs::create_dir(root.join("clog")).unwrap();
}

#[test]
fn snapshot_only_inspection_uses_actual_snapshot_and_zero_log_commits() {
    let mut fixture = seed(true);
    let baseline = observed(&fixture, |_| {}).unwrap();
    let snapshot = observed(&fixture, empty_logs).unwrap();
    assert_eq!(snapshot.committed_offset, baseline.committed_offset);
    assert_eq!(snapshot.state_blake3, baseline.state_blake3);
    assert_eq!(
        snapshot.current_program_digest,
        baseline.current_program_digest
    );
    assert_eq!(
        snapshot.observed_database_identity,
        baseline.observed_database_identity
    );
    assert_eq!(
        snapshot.observed_owner_identity,
        baseline.observed_owner_identity
    );
    assert_eq!(snapshot.input.commits, 0);
    assert_eq!(snapshot.framing, "snapshot-only-v1");
    assert_eq!(snapshot.snapshot.unwrap().offset, snapshot.committed_offset);
    assert!(matches!(
        observed(&fixture, |root| {
            empty_logs(root);
            fs::write(root.join("clog/orphan.ofs"), []).unwrap();
        }),
        Err(Refusal::Framing)
    ));
    assert!(matches!(
        observed(&fixture, |root| {
            let path = last_log(root);
            let header = fs::read(path).unwrap()[..10].to_vec();
            empty_logs(root);
            fs::write(root.join("clog/00000000000000000000.stdb.log"), header).unwrap();
        }),
        Err(Refusal::MissingHistory)
    ));
    fixture.request.history = History::Logs {};
    assert!(matches!(
        observed(&fixture, empty_logs),
        Err(Refusal::MissingHistory)
    ));
}
