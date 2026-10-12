use crate::protocol::{Refusal, Result};
use spacetimedb_lib::Identity;
use spacetimedb_snapshot::{
    BoxedPendingSnapshot, CompressionStats, ReconstructedSnapshot, SnapshotError, SnapshotRepo,
};
use spacetimedb_table::{blob_store::BlobStore, page_pool::PagePool, table::Table};
use std::ops::Range;
use std::sync::{
    Mutex,
    atomic::{AtomicBool, Ordering},
};

pub struct PinnedSnapshot {
    database: Identity,
    offset: u64,
    value: Mutex<Option<ReconstructedSnapshot>>,
    consumed: AtomicBool,
}

impl PinnedSnapshot {
    pub fn new(value: ReconstructedSnapshot) -> Self {
        Self {
            database: value.database_identity,
            offset: value.tx_offset,
            value: Mutex::new(Some(value)),
            consumed: AtomicBool::new(false),
        }
    }

    pub fn was_restored(&self) -> Result<()> {
        if !self.consumed.load(Ordering::Acquire) {
            return Err(Refusal::Snapshot);
        }
        Ok(())
    }
}

fn denied() -> SnapshotError {
    SnapshotError::Io(std::io::Error::new(
        std::io::ErrorKind::PermissionDenied,
        "passive snapshot",
    ))
}

impl SnapshotRepo for PinnedSnapshot {
    type Pending = BoxedPendingSnapshot;

    fn database_identity(&self) -> Identity {
        self.database
    }

    fn create_snapshot<'db>(
        &self,
        _: &mut dyn Iterator<Item = &'db mut Table>,
        _: &'db dyn BlobStore,
        _: u64,
    ) -> std::result::Result<Self::Pending, SnapshotError> {
        Err(denied())
    }

    fn read_snapshot(
        &self,
        offset: u64,
        _: &PagePool,
    ) -> std::result::Result<ReconstructedSnapshot, SnapshotError> {
        if offset != self.offset {
            return Err(denied());
        }
        let value = self
            .value
            .lock()
            .map_err(|_| denied())?
            .take()
            .ok_or_else(denied)?;
        self.consumed.store(true, Ordering::Release);
        Ok(value)
    }

    fn latest_snapshot_older_than(
        &self,
        upper: u64,
    ) -> std::result::Result<Option<u64>, SnapshotError> {
        Ok((self.offset <= upper).then_some(self.offset))
    }

    fn compress_snapshots(
        &self,
        _: &mut CompressionStats,
        _: Range<u64>,
    ) -> std::result::Result<(), SnapshotError> {
        Err(denied())
    }

    fn invalidate_newer_snapshots(&self, upper: u64) -> std::result::Result<(), SnapshotError> {
        if self.offset > upper {
            return Err(denied());
        }
        Ok(())
    }

    fn invalidate_snapshot(&self, _: u64) -> std::result::Result<(), SnapshotError> {
        Err(denied())
    }
}
