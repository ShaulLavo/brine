use crate::protocol::{Deadline, InputTotals, Limits, Refusal, Result, add};
use sha2::{Digest, Sha256};
use std::fs::{self, File, Metadata, OpenOptions};
use std::io::{Read, Seek, SeekFrom};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};

#[derive(Eq, PartialEq)]
pub struct FileSeal {
    pub path: PathBuf,
    device: u64,
    inode: u64,
    mode: u32,
    size: u64,
    hash: [u8; 32],
    pub expanded: u64,
}

pub fn files(root: &Path, limits: &Limits, deadline: &Deadline) -> Result<Vec<FileSeal>> {
    let root_meta = fs::symlink_metadata(root).map_err(|_| Refusal::UnsafeMember)?;
    if !root_meta.is_dir() || root_meta.mode() & 0o077 != 0 {
        return Err(Refusal::UnsafeMember);
    }
    let mut output = Vec::new();
    let mut pending = vec![(root.to_path_buf(), 0)];
    let mut nodes = 0;
    let mut stored = 0;
    let mut expanded = 0;
    while let Some((directory, depth)) = pending.pop() {
        visit(
            &directory,
            depth,
            &mut pending,
            &mut output,
            &mut nodes,
            &mut stored,
            limits,
            deadline,
        )?;
    }
    output.sort_unstable_by(|a, b| a.path.cmp(&b.path));
    for item in &mut output {
        let available = limits.expanded_bytes - expanded;
        item.expanded = expansion(&item.path, limits, available, deadline)?;
        add(
            &mut expanded,
            item.expanded,
            limits.expanded_bytes,
            Refusal::ExpansionLimit,
        )?;
    }
    Ok(output)
}

fn visit(
    directory: &Path,
    depth: u8,
    pending: &mut Vec<(PathBuf, u8)>,
    output: &mut Vec<FileSeal>,
    nodes: &mut u64,
    stored: &mut u64,
    limits: &Limits,
    deadline: &Deadline,
) -> Result<()> {
    if depth > 8 {
        return Err(Refusal::UnsafeMember);
    }
    check_owner(&fs::symlink_metadata(directory).map_err(|_| Refusal::UnsafeMember)?)?;
    for entry in fs::read_dir(directory).map_err(|_| Refusal::UnsafeMember)? {
        deadline.check()?;
        add(nodes, 1, limits.files * 2, Refusal::InputLimit)?;
        let path = entry.map_err(|_| Refusal::UnsafeMember)?.path();
        let meta = fs::symlink_metadata(&path).map_err(|_| Refusal::UnsafeMember)?;
        check_owner(&meta)?;
        if meta.is_dir() {
            pending.push((path, depth + 1));
            continue;
        }
        if !meta.is_file() || output.len() as u64 >= limits.files || meta.len() > limits.file_bytes
        {
            return Err(Refusal::UnsafeMember);
        }
        add(stored, meta.len(), limits.stored_bytes, Refusal::InputLimit)?;
        output.push(seal(&path, limits, deadline)?);
    }
    Ok(())
}

fn check_owner(meta: &Metadata) -> Result<()> {
    if meta.uid() != unsafe { libc::geteuid() }
        || (!meta.is_file() && !meta.is_dir())
        || (meta.is_file() && meta.nlink() != 1)
    {
        return Err(Refusal::UnsafeMember);
    }
    Ok(())
}

pub fn open(path: &Path) -> Result<File> {
    OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
        .map_err(|_| Refusal::UnsafeMember)
}

fn seal(path: &Path, limits: &Limits, deadline: &Deadline) -> Result<FileSeal> {
    let mut file = open(path)?;
    let before = file.metadata().map_err(|_| Refusal::UnsafeMember)?;
    check_owner(&before)?;
    if !before.is_file() || before.len() > limits.file_bytes {
        return Err(Refusal::InputLimit);
    }
    let mut hash = Sha256::new();
    let mut bytes = 0;
    let mut buffer = [0u8; 16384];
    loop {
        deadline.check()?;
        let count = file.read(&mut buffer).map_err(|_| Refusal::UnsafeMember)?;
        if count == 0 {
            break;
        }
        add(
            &mut bytes,
            count as u64,
            limits.file_bytes,
            Refusal::InputLimit,
        )?;
        hash.update(&buffer[..count]);
    }
    let after = file.metadata().map_err(|_| Refusal::UnsafeMember)?;
    if before.dev() != after.dev()
        || before.ino() != after.ino()
        || before.len() != bytes
        || after.len() != bytes
    {
        return Err(Refusal::ChangedCopy);
    }
    Ok(FileSeal {
        path: path.to_path_buf(),
        device: before.dev(),
        inode: before.ino(),
        mode: before.mode(),
        size: bytes,
        hash: hash.finalize().into(),
        expanded: 0,
    })
}

pub fn expansion(path: &Path, limits: &Limits, available: u64, deadline: &Deadline) -> Result<u64> {
    let mut file = open(path)?;
    let mut magic = [0; 4];
    let read = file.read(&mut magic).map_err(|_| Refusal::UnsafeMember)?;
    file.seek(SeekFrom::Start(0))
        .map_err(|_| Refusal::UnsafeMember)?;
    if read < 4 || magic != spacetimedb_fs_utils::compression::ZSTD_MAGIC_BYTES {
        let length = file.metadata().map_err(|_| Refusal::UnsafeMember)?.len();
        if length > limits.expanded_file_bytes.min(available) {
            return Err(Refusal::ExpansionLimit);
        }
        return Ok(length);
    }
    let mut decoder =
        zstd::stream::read::Decoder::new(file).map_err(|_| Refusal::ExpansionLimit)?;
    decoder
        .window_log_max(23)
        .map_err(|_| Refusal::ExpansionLimit)?;
    let mut buffer = [0u8; 16384];
    let mut total = 0;
    loop {
        deadline.check()?;
        let count = decoder
            .read(&mut buffer)
            .map_err(|_| Refusal::ExpansionLimit)?;
        if count == 0 {
            return Ok(total);
        }
        add(
            &mut total,
            count as u64,
            limits.expanded_file_bytes.min(available),
            Refusal::ExpansionLimit,
        )?;
    }
}

pub fn totals(files: &[FileSeal]) -> InputTotals {
    InputTotals {
        files: files.len() as u64,
        stored_bytes: files.iter().map(|f| f.size).sum(),
        expanded_bytes: files.iter().map(|f| f.expanded).sum(),
        commits: 0,
    }
}
