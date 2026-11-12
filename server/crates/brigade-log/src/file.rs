//! A log in one file.
//!
//! The file is a header and then frames: a length, a checksum of the record, the record. A position is the
//! offset after a frame, counted from the end of the header. A frame that is cut short or fails its checksum
//! at the end of the file is what a crash in the middle of an append leaves: opening the log ends it at the
//! last whole frame.

use std::fs::{File, OpenOptions};
use std::io::{Read, Seek, SeekFrom, Write};
use std::os::unix::fs::OpenOptionsExt;
use std::path::Path;

use crate::{Each, Log, LogError, Position};

/// What the file begins with: a name, the format's version and a newline.
const HEADER: &[u8; 8] = b"BRGLOG\x01\n";

/// The bytes before a record in its frame: its length and its checksum, four bytes each.
const FRAME_BYTES: u64 = 8;

/// The longest record the file log takes. A record of a Brigade server is a sealed message of at most a
/// few tens of kilobytes; this is only what keeps a damaged length from asking for memory.
const MAX_RECORD_BYTES: usize = 1 << 20;

/// A log in one file, private to its owner.
pub struct FileLog {
    file: File,
    /// The position of the log's end.
    end: Position,
}

impl FileLog {
    /// Opens the log at `path`, creating it when it is not there, and ends it at its last whole frame.
    ///
    /// # Errors
    /// When the file cannot be opened, or is not a log of this format.
    pub fn open(path: &Path) -> Result<Self, LogError> {
        let doing = "opening the file log";
        let mut file = OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .open(path)
            .map_err(|error| LogError::io(doing, &error))?;
        let length = file
            .metadata()
            .map_err(|error| LogError::io(doing, &error))?
            .len();
        if length == 0 {
            file.write_all(HEADER)
                .map_err(|error| LogError::io(doing, &error))?;
            file.sync_all().map_err(|error| LogError::io(doing, &error))?;
            return Ok(Self { file, end: 0 });
        }
        let mut header = [0_u8; HEADER.len()];
        file.seek(SeekFrom::Start(0))
            .map_err(|error| LogError::io(doing, &error))?;
        if file.read_exact(&mut header).is_err() || &header != HEADER {
            return Err(LogError::new(format!(
                "{} is not a Brigade file log of this version",
                path.display()
            )));
        }
        let mut log = Self { file, end: 0 };
        let (end, damaged) = log.scan(0, &mut |_, _| Ok(()))?;
        if damaged {
            // A frame that fails with whole frames' worth of file after it is not a torn append. Cutting the
            // file there would throw away every record after it, so the log is refused as it is.
            return Err(LogError::new(format!(
                "{} is damaged at position {end}: a record in the middle of the log fails its checksum",
                path.display()
            )));
        }
        // What follows the last whole frame is a torn append: cut it off, so the next append starts clean.
        if HEADER.len() as u64 + end < length {
            log.file
                .set_len(HEADER.len() as u64 + end)
                .map_err(|error| LogError::io(doing, &error))?;
            log.file.sync_all().map_err(|error| LogError::io(doing, &error))?;
        }
        log.end = end;
        Ok(log)
    }

    /// Reads whole frames from `from` and returns the position after the last of them, and whether it
    /// stopped at damage: a frame that fails although the file goes on after it. A frame that is cut short
    /// by the file's end, or that fails and is the last thing in the file, is a torn append and not damage.
    fn scan(&mut self, from: Position, each: &mut Each<'_>) -> Result<(Position, bool), LogError> {
        let doing = "reading the file log";
        let length = self
            .file
            .metadata()
            .map_err(|error| LogError::io(doing, &error))?
            .len();
        let mut position = from;
        self.file
            .seek(SeekFrom::Start(HEADER.len() as u64 + from))
            .map_err(|error| LogError::io(doing, &error))?;
        let mut frame = [0_u8; FRAME_BYTES as usize];
        let mut record = Vec::new();
        loop {
            let remaining = length.saturating_sub(HEADER.len() as u64 + position);
            if remaining < FRAME_BYTES {
                return Ok((position, false));
            }
            self.file
                .read_exact(&mut frame)
                .map_err(|error| LogError::io(doing, &error))?;
            let size = u32::from_le_bytes([frame[0], frame[1], frame[2], frame[3]]) as usize;
            let checksum = u32::from_le_bytes([frame[4], frame[5], frame[6], frame[7]]);
            if (size as u64) > remaining - FRAME_BYTES {
                return Ok((position, false));
            }
            if size > MAX_RECORD_BYTES {
                return Ok((position, true));
            }
            record.resize(size, 0);
            self.file
                .read_exact(&mut record)
                .map_err(|error| LogError::io(doing, &error))?;
            if crc32(&record) != checksum {
                return Ok((position, (size as u64) < remaining - FRAME_BYTES));
            }
            position += FRAME_BYTES + size as u64;
            each(&record, position)?;
        }
    }
}

impl Log for FileLog {
    fn append(&mut self, records: &[Vec<u8>]) -> Result<Position, LogError> {
        let doing = "appending to the file log";
        let mut frames = Vec::new();
        for record in records {
            if record.len() > MAX_RECORD_BYTES {
                return Err(LogError::new("a record is longer than the file log takes"));
            }
            // The cast is exact: the length was just held under a megabyte.
            frames.extend_from_slice(&(record.len() as u32).to_le_bytes());
            frames.extend_from_slice(&crc32(record).to_le_bytes());
            frames.extend_from_slice(record);
        }
        self.file
            .seek(SeekFrom::Start(HEADER.len() as u64 + self.end))
            .map_err(|error| LogError::io(doing, &error))?;
        self.file
            .write_all(&frames)
            .map_err(|error| LogError::io(doing, &error))?;
        self.file
            .sync_data()
            .map_err(|error| LogError::io(doing, &error))?;
        self.end += frames.len() as u64;
        Ok(self.end)
    }

    fn read_from(&mut self, from: Position, each: &mut Each<'_>) -> Result<Position, LogError> {
        if from > self.end {
            return Err(LogError::new(format!(
                "position {from} is past the log's end, {}: the snapshot is of another log",
                self.end
            )));
        }
        let (end, _) = self.scan(from, each)?;
        if end != self.end {
            return Err(LogError::new(format!(
                "position {from} is not the place after a record of this log"
            )));
        }
        Ok(end)
    }

    fn max_record_bytes(&self) -> usize {
        MAX_RECORD_BYTES
    }
}

/// The CRC-32 of zlib and Ethernet, bit by bit: a record is short and an append waits on the disk anyway.
fn crc32(bytes: &[u8]) -> u32 {
    let mut crc = u32::MAX;
    for byte in bytes {
        crc ^= u32::from(*byte);
        for _ in 0..8 {
            crc = if crc & 1 == 1 {
                (crc >> 1) ^ 0xEDB8_8320
            } else {
                crc >> 1
            };
        }
    }
    !crc
}

#[cfg(test)]
mod tests {
    use std::fs::OpenOptions;
    use std::io::Write;
    use std::path::PathBuf;

    use super::{FileLog, crc32};
    use crate::{Log, Position};

    /// A directory of the test's own, removed when the test ends.
    struct Scratch(PathBuf);

    impl Scratch {
        fn new(name: &str) -> Self {
            let dir = std::env::temp_dir().join(format!("brigade-log-{name}-{}", std::process::id()));
            let _ = std::fs::remove_dir_all(&dir);
            std::fs::create_dir_all(&dir).unwrap();
            Self(dir)
        }

        fn log(&self) -> PathBuf {
            self.0.join("records.log")
        }
    }

    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    fn read(log: &mut FileLog, from: Position) -> (Vec<(Vec<u8>, Position)>, Position) {
        let mut records = Vec::new();
        let end = log
            .read_from(from, &mut |record, after| {
                records.push((record.to_vec(), after));
                Ok(())
            })
            .unwrap();
        (records, end)
    }

    #[test]
    fn the_checksum_is_the_standard_one() {
        assert_eq!(crc32(b""), 0);
        assert_eq!(crc32(b"123456789"), 0xCBF4_3926);
    }

    #[test]
    fn records_come_back_in_order_from_any_position() {
        let scratch = Scratch::new("order");
        let mut log = FileLog::open(&scratch.log()).unwrap();
        assert_eq!(read(&mut log, 0), (vec![], 0));
        let first = log.append(&[b"one".to_vec(), b"".to_vec()]).unwrap();
        let second = log.append(&[b"three".to_vec()]).unwrap();
        assert!(second > first);

        let (records, end) = read(&mut log, 0);
        let bodies: Vec<&[u8]> = records.iter().map(|(record, _)| record.as_slice()).collect();
        assert_eq!(bodies, [&b"one"[..], b"", b"three"]);
        assert_eq!(end, second);
        assert_eq!(records[1].1, first);
        assert_eq!(records[2].1, second);
        // From the place after the first append, only what came after it.
        let (records, _) = read(&mut log, first);
        assert_eq!(records, vec![(b"three".to_vec(), second)]);
        assert_eq!(read(&mut log, second), (vec![], second));
    }

    #[test]
    fn a_log_is_the_same_after_it_is_opened_again() {
        let scratch = Scratch::new("reopen");
        let end = {
            let mut log = FileLog::open(&scratch.log()).unwrap();
            log.append(&[b"kept".to_vec(), vec![0xff; 70_000]]).unwrap()
        };
        let mut log = FileLog::open(&scratch.log()).unwrap();
        let (records, read_end) = read(&mut log, 0);
        assert_eq!(read_end, end);
        assert_eq!(records.len(), 2);
        assert_eq!(records[1].0.len(), 70_000);
        let next = log.append(&[b"more".to_vec()]).unwrap();
        assert!(next > end);
        assert_eq!(read(&mut log, end).0, vec![(b"more".to_vec(), next)]);
    }

    #[test]
    fn a_torn_append_is_cut_off_and_the_log_goes_on() {
        let scratch = Scratch::new("torn");
        let end = FileLog::open(&scratch.log())
            .unwrap()
            .append(&[b"whole".to_vec()])
            .unwrap();
        // A frame that names ten bytes and has three, as a crash in the middle of a write leaves.
        let mut file = OpenOptions::new().append(true).open(scratch.log()).unwrap();
        file.write_all(&10_u32.to_le_bytes()).unwrap();
        file.write_all(&0_u32.to_le_bytes()).unwrap();
        file.write_all(b"abc").unwrap();
        drop(file);

        let mut log = FileLog::open(&scratch.log()).unwrap();
        assert_eq!(read(&mut log, 0), (vec![(b"whole".to_vec(), end)], end));
        let next = log.append(&[b"after".to_vec()]).unwrap();
        drop(log);
        let mut log = FileLog::open(&scratch.log()).unwrap();
        let (records, _) = read(&mut log, 0);
        assert_eq!(records, vec![(b"whole".to_vec(), end), (b"after".to_vec(), next)]);
    }

    #[test]
    fn a_damaged_record_ends_the_log_where_it_is() {
        let scratch = Scratch::new("damaged");
        let mut log = FileLog::open(&scratch.log()).unwrap();
        let first = log.append(&[b"good".to_vec()]).unwrap();
        log.append(&[b"spoiled".to_vec()]).unwrap();
        drop(log);
        let mut bytes = std::fs::read(scratch.log()).unwrap();
        let last = bytes.len() - 1;
        bytes[last] ^= 1;
        std::fs::write(scratch.log(), bytes).unwrap();
        let mut log = FileLog::open(&scratch.log()).unwrap();
        assert_eq!(read(&mut log, 0), (vec![(b"good".to_vec(), first)], first));
    }

    #[test]
    fn damage_in_the_middle_refuses_the_log_and_cuts_nothing() {
        let scratch = Scratch::new("middle");
        let mut log = FileLog::open(&scratch.log()).unwrap();
        let first = log.append(&[b"good".to_vec()]).unwrap();
        log.append(&[b"spoiled".to_vec()]).unwrap();
        log.append(&[b"after it".to_vec()]).unwrap();
        drop(log);
        let mut bytes = std::fs::read(scratch.log()).unwrap();
        let length = bytes.len();
        // One bit of the second record, which has a whole record after it.
        let inside_second = super::HEADER.len() + first as usize + super::FRAME_BYTES as usize + 2;
        bytes[inside_second] ^= 1;
        std::fs::write(scratch.log(), &bytes).unwrap();
        let error = FileLog::open(&scratch.log()).err().unwrap();
        assert!(error.to_string().contains("damaged at position"), "{error}");
        assert_eq!(std::fs::read(scratch.log()).unwrap().len(), length);
    }

    #[test]
    fn a_position_of_another_log_is_refused() {
        let scratch = Scratch::new("position");
        let mut log = FileLog::open(&scratch.log()).unwrap();
        let end = log.append(&[b"record".to_vec()]).unwrap();
        assert!(log.read_from(end + 1, &mut |_, _| Ok(())).is_err());
        assert!(log.read_from(3, &mut |_, _| Ok(())).is_err());
    }

    #[test]
    fn a_file_that_is_not_a_log_is_refused() {
        let scratch = Scratch::new("foreign");
        std::fs::write(scratch.log(), b"something else entirely").unwrap();
        let error = FileLog::open(&scratch.log()).err().unwrap();
        assert!(error.to_string().contains("not a Brigade file log"));
    }
}
