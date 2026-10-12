use std::io::{self, Write};
use std::sync::atomic::{AtomicU8, Ordering};

const EMPTY: u8 = 0;
const OBSERVED: u8 = 1;
const REFUSAL: u8 = 2;

struct State(AtomicU8);

#[derive(Debug, Eq, PartialEq)]
enum Emission {
    Write,
    Abort,
    Wait,
}

impl State {
    const fn new() -> Self {
        Self(AtomicU8::new(EMPTY))
    }

    fn observed(&self) -> bool {
        self.0
            .compare_exchange(EMPTY, OBSERVED, Ordering::AcqRel, Ordering::Acquire)
            .is_ok()
    }

    fn refusal(&self) -> Emission {
        match self
            .0
            .compare_exchange(EMPTY, REFUSAL, Ordering::AcqRel, Ordering::Acquire)
        {
            Ok(_) => Emission::Write,
            Err(OBSERVED) => Emission::Abort,
            Err(_) => Emission::Wait,
        }
    }
}

static OUTPUT: State = State::new();

pub fn observed(bytes: &[u8]) -> io::Result<()> {
    if !OUTPUT.observed() {
        wait_for_exit();
    }
    io::stdout().lock().write_all(bytes)
}

pub fn terminate(message: &[u8]) -> ! {
    match OUTPUT.refusal() {
        Emission::Wait => wait_for_exit(),
        Emission::Abort => unsafe { libc::_exit(70) },
        Emission::Write => write_and_exit(message),
    }
}

fn write_and_exit(message: &[u8]) -> ! {
    unsafe {
        let flags = libc::fcntl(libc::STDOUT_FILENO, libc::F_GETFL);
        if flags >= 0 {
            libc::fcntl(libc::STDOUT_FILENO, libc::F_SETFL, flags | libc::O_NONBLOCK);
        }
        libc::write(libc::STDOUT_FILENO, message.as_ptr().cast(), message.len());
        libc::_exit(70);
    }
}

fn wait_for_exit() -> ! {
    loop {
        unsafe { libc::pause() };
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Barrier};

    #[test]
    fn concurrent_fatal_claims_have_one_writer_and_cannot_interrupt_its_message() {
        let state = Arc::new(State::new());
        let barrier = Arc::new(Barrier::new(17));
        let workers = (0..16)
            .map(|_| {
                let state = state.clone();
                let barrier = barrier.clone();
                std::thread::Builder::new()
                    .stack_size(65536)
                    .spawn(move || {
                        barrier.wait();
                        state.refusal()
                    })
                    .unwrap()
            })
            .collect::<Vec<_>>();
        barrier.wait();
        let claims = workers
            .into_iter()
            .map(|worker| worker.join().unwrap())
            .collect::<Vec<_>>();
        assert_eq!(
            claims
                .iter()
                .filter(|claim| **claim == Emission::Write)
                .count(),
            1
        );
        assert_eq!(
            claims
                .iter()
                .filter(|claim| **claim == Emission::Wait)
                .count(),
            15
        );
        assert!(!state.observed());
    }

    #[test]
    fn deadline_aborts_an_observed_writer_without_appending_another_response() {
        let state = State::new();
        assert!(state.observed());
        assert!(!state.observed());
        assert_eq!(state.refusal(), Emission::Abort);
    }
}
