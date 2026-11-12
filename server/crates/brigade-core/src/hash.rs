//! The one digest the rules use, and the text forms of what the caller's randomness becomes.

use ring::digest::{SHA256, digest};

const HEX: &[u8; 16] = b"0123456789abcdef";

/// Lowercase hexadecimal of `bytes`.
#[must_use]
pub fn hex(bytes: &[u8]) -> String {
    let mut text = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        text.push(char::from(HEX[usize::from(byte >> 4)]));
        text.push(char::from(HEX[usize::from(byte & 0x0f)]));
    }
    text
}

/// The SHA-256 of `text`, as lowercase hexadecimal. It is how the server keeps a credential and a join
/// secret: the hash, never the thing (the contract's sections 1 and 3).
#[must_use]
pub fn sha256_hex(text: &str) -> String {
    hex(digest(&SHA256, text.as_bytes()).as_ref())
}

/// The SHA-256 of `bytes`, as lowercase hexadecimal.
#[must_use]
pub fn sha256_hex_of(bytes: &[u8]) -> String {
    hex(digest(&SHA256, bytes).as_ref())
}

/// Whether two strings are equal, in time that does not depend on where they differ.
#[must_use]
pub fn same(left: &str, right: &str) -> bool {
    if left.len() != right.len() {
        return false;
    }
    left.bytes()
        .zip(right.bytes())
        .fold(0, |acc, (a, b)| acc | (a ^ b))
        == 0
}

/// The source of every new identifier, credential and join secret.
///
/// The rules draw nothing themselves: the caller hands them a source, the operating system's in the server
/// and a counter in a test.
pub trait Entropy {
    /// Fills `buffer` with bytes nobody can predict.
    fn fill(&mut self, buffer: &mut [u8]);
}

/// An opaque identifier: sixteen bytes from `entropy`, as hexadecimal.
pub fn new_ref(entropy: &mut dyn Entropy) -> String {
    let mut bytes = [0_u8; 16];
    entropy.fill(&mut bytes);
    hex(&bytes)
}

/// A credential: thirty-two bytes from `entropy`, as hexadecimal. Sixty-four characters of the contract's
/// alphabet (section 1).
pub fn new_credential(entropy: &mut dyn Entropy) -> String {
    let mut bytes = [0_u8; 32];
    entropy.fill(&mut bytes);
    hex(&bytes)
}

#[cfg(test)]
mod tests {
    use super::{hex, same, sha256_hex};

    #[test]
    fn hashes_as_the_standard_says() {
        assert_eq!(
            sha256_hex(""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        assert_eq!(
            sha256_hex("abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }

    #[test]
    fn renders_bytes_as_lowercase_hexadecimal() {
        assert_eq!(hex(&[0x00, 0x0f, 0xa5, 0xff]), "000fa5ff");
    }

    #[test]
    fn compares_whole_strings() {
        assert!(same("abc", "abc"));
        assert!(!same("abc", "abd"));
        assert!(!same("abc", "ab"));
        assert!(same("", ""));
    }
}
