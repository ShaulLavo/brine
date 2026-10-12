use serde::{Deserialize, Serialize};
use std::time::{Duration, Instant};

pub const FORMAT: &str = "brine-native-reader-v1";
pub const UPSTREAM: &str = "6b6e0ab19e4f8f52472aa4379552e9593c8f53bb";
pub const ALGORITHM: &str = "bsatn-row-digest-multiset-blake3-v1";
pub type Result<T> = std::result::Result<T, Refusal>;

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Refusal {
    Request,
    Containment,
    UnsafeMember,
    InputLimit,
    ExpansionLimit,
    Framing,
    MissingHistory,
    Snapshot,
    Identity,
    Native,
    StateLimit,
    Program,
    OutputLimit,
    ChangedCopy,
    Deadline,
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub format: String,
    pub expected_database_identity: String,
    pub expected_owner_identity: String,
    pub history: History,
    pub limits: Limits,
}

#[derive(Clone, Copy, Deserialize, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum History {
    Logs {},
    Snapshot { offset: u64, replica_id: u64 },
}

#[derive(Clone, Copy, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Limits {
    pub files: u64,
    pub stored_bytes: u64,
    pub file_bytes: u64,
    pub expanded_bytes: u64,
    pub expanded_file_bytes: u64,
    pub commit_bytes: u64,
    pub commits: u64,
    pub tables: u64,
    pub rows: u64,
    pub row_bytes: u64,
    pub state_bytes: u64,
    pub program_bytes: u64,
    pub output_bytes: u64,
    pub memory_bytes: u64,
    pub allocation_bytes: u64,
    pub deadline_ms: u64,
}

impl Limits {
    pub const MAX: Self = Self {
        files: 8192,
        stored_bytes: 256 << 20,
        file_bytes: 64 << 20,
        expanded_bytes: 256 << 20,
        expanded_file_bytes: 64 << 20,
        commit_bytes: 8 << 20,
        commits: 1_000_000,
        tables: 512,
        rows: 100_000,
        row_bytes: 8 << 20,
        state_bytes: 128 << 20,
        program_bytes: 8 << 20,
        output_bytes: 1 << 20,
        memory_bytes: 384 << 20,
        allocation_bytes: 64 << 20,
        deadline_ms: 60_000,
    };

    pub fn validate(&self) -> Result<()> {
        let current = serde_json::to_value(self).map_err(|_| Refusal::Request)?;
        let upper = serde_json::to_value(Self::MAX).map_err(|_| Refusal::Request)?;
        for (key, value) in current.as_object().ok_or(Refusal::Request)? {
            let count = value.as_u64().ok_or(Refusal::Request)?;
            if count == 0 || count > upper[key].as_u64().ok_or(Refusal::Request)? {
                return Err(Refusal::Request);
            }
        }
        if self.memory_bytes < 8 << 20
            || self.allocation_bytes > self.memory_bytes
            || self.output_bytes < 256
        {
            return Err(Refusal::Request);
        }
        Ok(())
    }
}

pub struct Deadline {
    start: Instant,
    duration: Duration,
}

impl Deadline {
    pub fn new(ms: u64) -> Self {
        Self {
            start: Instant::now(),
            duration: Duration::from_millis(ms),
        }
    }

    pub fn check(&self) -> Result<()> {
        if self.start.elapsed() >= self.duration {
            return Err(Refusal::Deadline);
        }
        Ok(())
    }
}

#[derive(Serialize)]
pub struct Evidence {
    pub format: &'static str,
    pub outcome: &'static str,
    pub upstream_commit: &'static str,
    pub observed_database_identity: String,
    pub observed_owner_identity: String,
    pub current_program_kind: &'static str,
    pub current_program_algorithm: &'static str,
    pub current_program_digest: String,
    pub current_program_hash: String,
    pub current_program_sha256: String,
    pub program_bytes: u64,
    pub committed_offset: u64,
    pub framing: &'static str,
    pub snapshot: Option<SnapshotEvidence>,
    pub persisted_connected_clients: u64,
    pub evidence_algorithm: &'static str,
    pub state_blake3: String,
    pub tables: Vec<TableEvidence>,
    pub confirmed_pairs_projection: Option<crate::projection::Projection>,
    pub input: InputTotals,
    pub limits: Limits,
}

#[derive(Serialize)]
pub struct SnapshotEvidence {
    pub observed_database_identity: String,
    pub observed_replica_id: u64,
    pub offset: u64,
}

#[derive(Serialize)]
pub struct TableEvidence {
    pub table_id: u32,
    pub name: String,
    pub table_type: &'static str,
    pub rows: u64,
    pub bsatn_bytes: u64,
    pub multiset_blake3: String,
}

#[derive(Default, Serialize)]
pub struct InputTotals {
    pub files: u64,
    pub stored_bytes: u64,
    pub expanded_bytes: u64,
    pub commits: u64,
}

pub fn add(total: &mut u64, count: u64, maximum: u64, refusal: Refusal) -> Result<()> {
    *total = total
        .checked_add(count)
        .filter(|sum| *sum <= maximum)
        .ok_or(refusal)?;
    Ok(())
}
