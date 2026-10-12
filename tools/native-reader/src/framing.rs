use crate::input::FileSeal;
use crate::protocol::{Deadline, Limits, Refusal, Result, add};
use spacetimedb_commitlog::{
    CommittedMeta, StoredCommit,
    repo::{Fs, Repo},
    segment,
};
use spacetimedb_paths::server::ReplicaDir;
use std::io::{Cursor, Read};

pub struct Head {
    pub first: u64,
    pub last: u64,
    pub commits: u64,
}

pub fn inspect(
    root: &ReplicaDir,
    files: &[FileSeal],
    limits: &Limits,
    deadline: &Deadline,
) -> Result<Option<Head>> {
    let repo = Fs::new(root.commit_log(), None).map_err(|_| Refusal::Native)?;
    let offsets = repo.existing_offsets().map_err(|_| Refusal::Native)?;
    if offsets.is_empty() {
        let mut entries = std::fs::read_dir(root.commit_log()).map_err(|_| Refusal::Framing)?;
        if entries.next().is_some() {
            return Err(Refusal::Framing);
        }
        return Ok(None);
    }
    let first = *offsets.first().ok_or(Refusal::MissingHistory)?;
    let mut next = first;
    let mut commits = 0;
    for offset in offsets {
        deadline.check()?;
        let path = repo.segment_path(offset);
        let segment_path: &std::path::Path = path.as_ref();
        let seal = files
            .iter()
            .find(|file| file.path == segment_path)
            .ok_or(Refusal::ChangedCopy)?;
        let mut reader = repo
            .open_segment_reader(offset)
            .map_err(|_| Refusal::Framing)?;
        let mut bytes = vec![0; seal.expanded as usize];
        reader
            .read_exact(&mut bytes)
            .map_err(|_| Refusal::Framing)?;
        let mut extra = [0];
        if reader.read(&mut extra).map_err(|_| Refusal::Framing)? != 0 {
            return Err(Refusal::ChangedCopy);
        }
        segment(&bytes, &mut next, &mut commits, limits, deadline)?;
    }
    if commits == 0 {
        return Err(Refusal::MissingHistory);
    }
    match spacetimedb_commitlog::committed_meta(root.commit_log()).map_err(|_| Refusal::Framing)? {
        Some(CommittedMeta::Complete { metadata }) if metadata.tx_range.end == next => {}
        _ => return Err(Refusal::Framing),
    }
    Ok(Some(Head {
        first,
        last: next - 1,
        commits,
    }))
}

fn segment(
    bytes: &[u8],
    next: &mut u64,
    commits: &mut u64,
    limits: &Limits,
    deadline: &Deadline,
) -> Result<()> {
    let mut cursor = Cursor::new(bytes);
    let header = segment::Header::decode(&mut cursor).map_err(|_| Refusal::Framing)?;
    if header.log_format_version != 1 || header.checksum_algorithm != 0 {
        return Err(Refusal::Framing);
    }
    while (cursor.position() as usize) < bytes.len() {
        deadline.check()?;
        if padding(bytes, cursor.position() as usize)? {
            break;
        }
        let commit = bounded_commit(&mut cursor, bytes, limits)?;
        if commit.min_tx_offset != *next || commit.n == 0 {
            return Err(Refusal::Framing);
        }
        *next = next.checked_add(commit.n as u64).ok_or(Refusal::Framing)?;
        add(commits, 1, limits.commits, Refusal::Framing)?;
    }
    Ok(())
}

fn padding(bytes: &[u8], pos: usize) -> Result<bool> {
    let prefix = &bytes[pos..bytes
        .len()
        .min(pos + spacetimedb_commitlog::commit::Header::LEN)];
    if prefix.iter().any(|byte| *byte != 0) {
        return Ok(false);
    }
    if bytes[pos..].iter().any(|byte| *byte != 0) {
        return Err(Refusal::Framing);
    }
    Ok(true)
}

fn bounded_commit(
    cursor: &mut Cursor<&[u8]>,
    bytes: &[u8],
    limits: &Limits,
) -> Result<StoredCommit> {
    let pos = cursor.position() as usize;
    if bytes.len() - pos < spacetimedb_commitlog::commit::Header::LEN {
        return Err(Refusal::Framing);
    }
    let header = spacetimedb_commitlog::commit::Header::decode(&mut *cursor)
        .map_err(|_| Refusal::Framing)?
        .ok_or(Refusal::Framing)?;
    let framing = spacetimedb_commitlog::commit::Header::LEN + 4;
    let full = (header.len as usize)
        .checked_add(framing)
        .ok_or(Refusal::Framing)?;
    if header.len as u64 > limits.commit_bytes || full > bytes.len() - pos {
        return Err(Refusal::Framing);
    }
    cursor.set_position(pos as u64);
    StoredCommit::decode(cursor)
        .map_err(|_| Refusal::Framing)?
        .ok_or(Refusal::Framing)
}
