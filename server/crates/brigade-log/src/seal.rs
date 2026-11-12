//! The seal on a record: AES-256-GCM under a key the operator gives every instance in a file.

use ring::aead::{AES_256_GCM, Aad, LessSafeKey, NONCE_LEN, Nonce, UnboundKey};
use ring::rand::{SecureRandom, SystemRandom};

use crate::LogError;

/// The length of the key, in bytes.
pub const KEY_BYTES: usize = 32;

/// What every sealed record is bound to, so a sealed record of another format is not opened as this one.
const CONTEXT: &[u8] = b"brigade-record/1";

/// Seals and opens records under one key. A sealed record is a nonce, the ciphertext and its tag.
pub struct Sealer {
    key: LessSafeKey,
    random: SystemRandom,
}

impl Sealer {
    /// A sealer under `key`.
    ///
    /// # Errors
    /// Never for a key of [`KEY_BYTES`] bytes.
    pub fn new(key: &[u8; KEY_BYTES]) -> Result<Self, LogError> {
        let key =
            UnboundKey::new(&AES_256_GCM, key).map_err(|_| LogError::new("the record key was refused"))?;
        Ok(Self {
            key: LessSafeKey::new(key),
            random: SystemRandom::new(),
        })
    }

    /// How many bytes sealing adds to a record.
    #[must_use]
    pub fn overhead() -> usize {
        NONCE_LEN + AES_256_GCM.tag_len()
    }

    /// Seals one record.
    ///
    /// # Errors
    /// When the operating system gives no randomness for the nonce.
    pub fn seal(&self, record: &[u8]) -> Result<Vec<u8>, LogError> {
        let mut nonce = [0_u8; NONCE_LEN];
        self.random
            .fill(&mut nonce)
            .map_err(|_| LogError::new("no randomness for a record's nonce"))?;
        let mut sealed = Vec::with_capacity(record.len() + Self::overhead());
        sealed.extend_from_slice(&nonce);
        sealed.extend_from_slice(record);
        let tag = self
            .key
            .seal_in_place_separate_tag(
                Nonce::assume_unique_for_key(nonce),
                Aad::from(CONTEXT),
                &mut sealed[NONCE_LEN..],
            )
            .map_err(|_| LogError::new("a record could not be sealed"))?;
        sealed.extend_from_slice(tag.as_ref());
        Ok(sealed)
    }

    /// Opens one sealed record.
    ///
    /// # Errors
    /// When the bytes were not sealed under this key, or were changed since: a reader treats that as a
    /// record that is not there.
    pub fn open(&self, sealed: &[u8]) -> Result<Vec<u8>, LogError> {
        let failed =
            || LogError::new("a record failed its seal: it was damaged, or sealed under another key");
        if sealed.len() < Self::overhead() {
            return Err(failed());
        }
        let (nonce, body) = sealed.split_at(NONCE_LEN);
        let nonce = Nonce::try_assume_unique_for_key(nonce).map_err(|_| failed())?;
        let mut body = body.to_vec();
        let length = self
            .key
            .open_in_place(nonce, Aad::from(CONTEXT), &mut body)
            .map_err(|_| failed())?
            .len();
        body.truncate(length);
        Ok(body)
    }
}

#[cfg(test)]
mod tests {
    use super::{KEY_BYTES, Sealer};

    #[test]
    fn opens_what_it_sealed_and_nothing_else() {
        let sealer = Sealer::new(&[7; KEY_BYTES]).unwrap();
        let sealed = sealer.seal(b"a record").unwrap();
        assert_eq!(sealed.len(), b"a record".len() + Sealer::overhead());
        assert_eq!(sealer.open(&sealed).unwrap(), b"a record");

        // Two seals of one record differ: the nonce is new each time.
        assert_ne!(sealer.seal(b"a record").unwrap(), sealed);

        // One changed bit anywhere is refused.
        for index in 0..sealed.len() {
            let mut damaged = sealed.clone();
            damaged[index] ^= 1;
            assert!(sealer.open(&damaged).is_err(), "byte {index}");
        }
        assert!(sealer.open(&sealed[..sealed.len() - 1]).is_err());
        assert!(sealer.open(b"short").is_err());
        // Another key does not open it.
        assert!(Sealer::new(&[8; KEY_BYTES]).unwrap().open(&sealed).is_err());
    }

    #[test]
    fn seals_an_empty_record() {
        let sealer = Sealer::new(&[1; KEY_BYTES]).unwrap();
        let sealed = sealer.seal(b"").unwrap();
        assert_eq!(sealer.open(&sealed).unwrap(), b"");
    }
}
