//! The ordered log of a Brigade server.
//!
//! The server is a state machine over one ordered log (`.context/plans/brigade-server.md`, section 4.2): a
//! request becomes records, the records are appended and synced, and only then is the request answered. This
//! crate is that log behind one trait, [`Log`], with two implementations:
//!
//! - [`FileLog`], one file of framed records. It is what the tests run on, and what a build without Cavalry
//!   has.
//! - `CavalryLog` (feature `cavalry`), a recorded Cavalry publication: the feed a standby follows.
//!
//! [`Sealer`] seals each record before it is appended. The seal is for the link between instances, which
//! Cavalry does not encrypt, and it lets a reader tell a record that was damaged from one that was written.
//!
//! The trait has `append` and `read_from`. A standby's `follow` comes with the standby (the plan's phase 4):
//! its request travels over HTTP between servers and names a session, so its shape is that phase's to fix.

mod file;
mod seal;

#[cfg(feature = "cavalry")]
mod feed;

pub use file::FileLog;
pub use seal::{KEY_BYTES, Sealer};

#[cfg(feature = "cavalry")]
pub use feed::{CavalryLog, FeedOptions};

use std::fmt;

/// A place in the log: zero is its beginning, and every other value is the place after a record. A position
/// is only ever one the log handed out.
pub type Position = u64;

/// A failure of the log. The message names what went wrong and, where it helps, what to do about it.
#[derive(Debug)]
pub struct LogError(String);

impl LogError {
    /// A failure with this message.
    #[must_use]
    pub fn new(message: impl Into<String>) -> Self {
        Self(message.into())
    }

    /// A failure of an I/O call, with what was being done.
    #[must_use]
    pub fn io(doing: &str, error: &std::io::Error) -> Self {
        Self(format!("{doing}: {error}"))
    }
}

impl fmt::Display for LogError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl std::error::Error for LogError {}

/// What a read hands each record to, with the position after it. A refusal ends the read.
pub type Each<'a> = dyn FnMut(&[u8], Position) -> Result<(), LogError> + 'a;

/// One ordered, durable log of records.
pub trait Log {
    /// Appends the records in order and returns the position after the last of them. It returns once they
    /// are on the disk: what the caller then answers is true after a power loss.
    ///
    /// # Errors
    /// When the records could not be made durable. The caller must not go on: its state would be ahead of
    /// the log.
    fn append(&mut self, records: &[Vec<u8>]) -> Result<Position, LogError>;

    /// Hands `each` every record after `from`, in order, with the position after it, and returns the
    /// position of the log's end.
    ///
    /// # Errors
    /// When the log cannot be read from `from`, or `each` refuses a record.
    fn read_from(&mut self, from: Position, each: &mut Each<'_>) -> Result<Position, LogError>;

    /// The longest record the log takes, in bytes.
    fn max_record_bytes(&self) -> usize;

    /// Tells the log that a snapshot the server keeps stands for every record before `before`, so the log
    /// may let go of them. A log that keeps everything does nothing.
    ///
    /// # Errors
    /// When the log could not go on after it began to let go. What it kept is still whole.
    fn release(&mut self, before: Position) -> Result<(), LogError> {
        let _ = before;
        Ok(())
    }

    /// What the log has to tell the operator since it was last asked: a sentence each. The caller writes
    /// them where the server's own lines go.
    fn notes(&mut self) -> Vec<String> {
        Vec::new()
    }
}
