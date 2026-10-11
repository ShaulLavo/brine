mod framing;
mod input;
mod memory;
mod protocol;
mod snapshot;
mod state;

use protocol::{ALGORITHM, Deadline, Evidence, FORMAT, Refusal, Request, Result, UPSTREAM};
use serde::Serialize;
use spacetimedb_lib::Identity;
use spacetimedb_paths::{FromPathUnchecked, server::ReplicaDir};
use std::io::{Read, Write};
use std::path::Path;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};

#[global_allocator]
static ALLOCATOR: memory::Bounded = memory::Bounded;

#[derive(Serialize)]
struct Failure {
    format: &'static str,
    outcome: &'static str,
    code: Refusal,
}

fn main() {
    std::panic::set_hook(Box::new(|_| {}));
    let outcome = std::panic::catch_unwind(run).unwrap_or(Err(Refusal::Native));
    let code = match outcome {
        Ok(()) => 0,
        Err(code) => {
            write_refusal(code);
            70
        }
    };
    std::process::exit(code);
}

fn write_refusal(code: Refusal) {
    let failure = Failure {
        format: FORMAT,
        outcome: "refused",
        code,
    };
    let bytes = serde_json::to_vec(&failure).unwrap_or_default();
    unsafe {
        let flags = libc::fcntl(libc::STDOUT_FILENO, libc::F_GETFL);
        if flags >= 0 {
            libc::fcntl(libc::STDOUT_FILENO, libc::F_SETFL, flags | libc::O_NONBLOCK);
        }
    }
    let mut output = std::io::stdout().lock();
    let _ = output.write_all(&bytes);
    let _ = output.write_all(b"\n");
    let _ = output.flush();
}

fn run() -> Result<()> {
    contained()?;
    address_space()?;
    let hard_done = watchdog(protocol::Limits::MAX.deadline_ms)?;
    let mut bytes = Vec::new();
    std::io::stdin()
        .take(8193)
        .read_to_end(&mut bytes)
        .map_err(|_| Refusal::Request)?;
    if bytes.len() > 8192 {
        return Err(Refusal::Request);
    }
    let request: Request = serde_json::from_slice(&bytes).map_err(|_| Refusal::Request)?;
    if request.format != FORMAT {
        return Err(Refusal::Request);
    }
    request.limits.validate()?;
    memory::configure(request.limits.memory_bytes, request.limits.allocation_bytes);
    let database = identity(&request.expected_database_identity)?;
    let owner = identity(&request.expected_owner_identity)?;
    let deadline = Deadline::new(request.limits.deadline_ms);
    let done = watchdog(request.limits.deadline_ms)?;
    let evidence = inspect(
        Path::new("/copy/replica"),
        &request,
        database,
        owner,
        &deadline,
    )?;
    let mut writer = BoundedOutput {
        bytes: Vec::new(),
        maximum: request.limits.output_bytes as usize,
    };
    serde_json::to_writer(&mut writer, &evidence).map_err(|_| Refusal::OutputLimit)?;
    deadline.check()?;
    std::io::stdout()
        .lock()
        .write_all(&writer.bytes)
        .map_err(|_| Refusal::OutputLimit)?;
    deadline.check()?;
    done.store(true, Ordering::Release);
    hard_done.store(true, Ordering::Release);
    Ok(())
}

fn inspect(
    root: &Path,
    request: &Request,
    database: Identity,
    owner: Identity,
    deadline: &Deadline,
) -> Result<Evidence> {
    shape(root)?;
    let before = input::files(root, &request.limits, deadline)?;
    let root = ReplicaDir::from_path_unchecked(root.to_path_buf());
    let head = framing::inspect(&root, &before, &request.limits, deadline)?;
    let recovered = state::observe(
        &root,
        database,
        owner,
        request.history,
        &head,
        &request.limits,
        deadline,
    )?;
    let after = input::files(root.as_ref(), &request.limits, deadline)?;
    if before != after {
        return Err(Refusal::ChangedCopy);
    }
    let mut totals = input::totals(&before);
    totals.commits = head.commits;
    Ok(Evidence {
        format: FORMAT,
        outcome: "observed",
        upstream_commit: UPSTREAM,
        observed_database_identity: recovered.database,
        observed_owner_identity: recovered.owner,
        current_program_kind: recovered.program_kind,
        current_program_algorithm: "keccak-256",
        current_program_digest: recovered.program_digest,
        current_program_hash: recovered.program_wire,
        current_program_sha256: recovered.program_sha256,
        program_bytes: recovered.program_bytes,
        committed_offset: head.last,
        framing: "complete-v1",
        snapshot: recovered.snapshot,
        persisted_connected_clients: recovered.connected,
        evidence_algorithm: ALGORITHM,
        state_blake3: recovered.hash,
        tables: recovered.tables,
        input: totals,
        limits: request.limits,
    })
}

fn shape(root: &Path) -> Result<()> {
    if !root.join("clog").is_dir() {
        return Err(Refusal::MissingHistory);
    }
    for entry in std::fs::read_dir(root).map_err(|_| Refusal::UnsafeMember)? {
        let entry = entry.map_err(|_| Refusal::UnsafeMember)?;
        let name = entry.file_name();
        if name != "clog" && name != "snapshots" && name != "db.lock" {
            return Err(Refusal::UnsafeMember);
        }
    }
    Ok(())
}

fn identity(text: &str) -> Result<Identity> {
    if text.len() != 64
        || text
            .bytes()
            .any(|byte| !byte.is_ascii_digit() && !(b'a'..=b'f').contains(&byte))
    {
        return Err(Refusal::Request);
    }
    text.parse().map_err(|_| Refusal::Request)
}

fn address_space() -> Result<()> {
    let mut limit = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    unsafe {
        if libc::getrlimit(libc::RLIMIT_AS, &mut limit) != 0 {
            return Err(Refusal::Native);
        }
        limit.rlim_cur = limit.rlim_cur.min(1 << 30);
        if libc::setrlimit(libc::RLIMIT_AS, &limit) != 0 {
            return Err(Refusal::Native);
        }
    }
    Ok(())
}

fn watchdog(milliseconds: u64) -> Result<Arc<AtomicBool>> {
    let done = Arc::new(AtomicBool::new(false));
    let stopped = done.clone();
    std::thread::Builder::new().stack_size(65536).spawn(move || {
        std::thread::sleep(std::time::Duration::from_millis(milliseconds));
        if !stopped.load(Ordering::Acquire) {
            memory::terminate(b"{\"format\":\"brine-native-reader-v1\",\"outcome\":\"refused\",\"code\":\"deadline\"}\n");
        }
    }).map_err(|_| Refusal::Native)?;
    Ok(done)
}

fn contained() -> Result<()> {
    if std::env::args_os().count() != 1
        || Path::new("/work").exists()
        || Path::new("/home").exists()
    {
        return Err(Refusal::Containment);
    }
    if std::env::vars_os().any(|(key, _)| {
        !matches!(
            key.to_str(),
            Some(
                "PATH"
                    | "LANG"
                    | "LC_ALL"
                    | "LC_CTYPE"
                    | "TZ"
                    | "HOME"
                    | "TMPDIR"
                    | "HOSTNAME"
                    | "PWD"
            )
        )
    }) {
        return Err(Refusal::Containment);
    }
    descriptors()?;
    let devices = bounded_text("/proc/net/dev", 4096)?;
    if devices
        .lines()
        .skip(2)
        .any(|line| line.split(':').next().map(str::trim) != Some("lo"))
    {
        return Err(Refusal::Containment);
    }
    let mounts = bounded_text("/proc/self/mountinfo", 65536)?;
    let mut root_readonly = false;
    let mut copy = false;
    for line in mounts.lines() {
        let fields = line.split_whitespace().collect::<Vec<_>>();
        if fields.len() < 7 {
            return Err(Refusal::Containment);
        }
        let point = fields[4];
        if !allowed_mount(point) {
            return Err(Refusal::Containment);
        }
        if point == "/" {
            root_readonly = fields[5].split(',').any(|value| value == "ro");
        }
        if point == "/copy" || point == "/copy/replica" {
            copy = true;
        }
    }
    if !root_readonly || !copy {
        return Err(Refusal::Containment);
    }
    Ok(())
}

fn descriptors() -> Result<()> {
    let directory = format!("/proc/{}/fd", std::process::id());
    for entry in std::fs::read_dir("/proc/self/fd").map_err(|_| Refusal::Containment)? {
        let entry = entry.map_err(|_| Refusal::Containment)?;
        let number: u32 = entry
            .file_name()
            .to_str()
            .ok_or(Refusal::Containment)?
            .parse()
            .map_err(|_| Refusal::Containment)?;
        if number < 3 {
            continue;
        }
        if std::fs::read_link(entry.path()).map_err(|_| Refusal::Containment)?
            != Path::new(&directory)
        {
            return Err(Refusal::Containment);
        }
    }
    Ok(())
}

fn allowed_mount(point: &str) -> bool {
    matches!(
        point,
        "/" | "/copy"
            | "/copy/replica"
            | "/reader"
            | "/tmp"
            | "/usr"
            | "/sys"
            | "/sys/fs/cgroup"
            | "/etc/hostname"
            | "/etc/hosts"
            | "/etc/resolv.conf"
    ) || point == "/proc"
        || point.starts_with("/proc/")
        || point == "/dev"
        || point.starts_with("/dev/")
}

fn bounded_text(path: &str, maximum: u64) -> Result<String> {
    let mut bytes = Vec::new();
    std::fs::File::open(path)
        .map_err(|_| Refusal::Containment)?
        .take(maximum + 1)
        .read_to_end(&mut bytes)
        .map_err(|_| Refusal::Containment)?;
    if bytes.len() as u64 > maximum {
        return Err(Refusal::Containment);
    }
    String::from_utf8(bytes).map_err(|_| Refusal::Containment)
}

struct BoundedOutput {
    bytes: Vec<u8>,
    maximum: usize,
}

impl Write for BoundedOutput {
    fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
        let length = self
            .bytes
            .len()
            .checked_add(bytes.len())
            .filter(|size| *size <= self.maximum)
            .ok_or_else(|| std::io::Error::other("output limit"))?;
        self.bytes
            .try_reserve_exact(length - self.bytes.len())
            .map_err(|_| std::io::Error::other("output allocation"))?;
        self.bytes.extend_from_slice(bytes);
        Ok(bytes.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[cfg(test)]
mod tests;
