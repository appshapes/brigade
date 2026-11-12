//! TLS, when the server serves it itself.
//!
//! The operator gives the server a certificate chain and its key as two PEM files. The other way to run a
//! server off loopback is `--tls-in-front`: plain HTTP behind something that ends TLS.
//!
//! A certificate no authority signed is trusted by its fingerprint: a team file names it, and a member's
//! adapter then accepts that certificate and no other. So the server says the fingerprint when it starts,
//! and with `--tls-self-signed` it makes such a certificate itself, once, so that an operator needs no other
//! tool to serve HTTPS.

use std::fs::{self, DirBuilder, OpenOptions};
use std::io::Write as _;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use brigade_core::hash::sha256_hex_of;
use rustls::ServerConfig;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use tokio_rustls::TlsAcceptor;

/// What accepts a TLS connection, and the fingerprint of the certificate it presents.
pub struct Serving {
    pub acceptor: TlsAcceptor,
    /// `sha256:` and the SHA-256 of the server's own certificate as it is sent (DER), in lowercase hex: what
    /// `openssl x509 -noout -fingerprint -sha256` prints, without its colons.
    pub fingerprint: String,
}

/// What accepts a TLS connection with the certificate chain in `certificate` and the key in `key`.
///
/// The chain's first certificate is the server's own, as TLS sends it, and the fingerprint is that one's.
///
/// # Errors
/// A sentence for the operator. It names the file and never what the file holds.
pub fn serving(certificate: &Path, key: &Path) -> Result<Serving, String> {
    let chain: Vec<CertificateDer<'static>> = CertificateDer::pem_file_iter(certificate)
        .and_then(Iterator::collect)
        .map_err(|error| {
            format!(
                "{} does not read as a certificate chain: {error}",
                certificate.display()
            )
        })?;
    let Some(own) = chain.first() else {
        return Err(format!("{} holds no certificate", certificate.display()));
    };
    let fingerprint = format!("sha256:{}", sha256_hex_of(own.as_ref()));
    let key = PrivateKeyDer::from_pem_file(key)
        .map_err(|error| format!("{} does not read as a private key: {error}", key.display()))?;
    let mut config = ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
        .with_safe_default_protocol_versions()
        .map_err(|error| format!("TLS could not be set up: {error}"))?
        .with_no_client_auth()
        .with_single_cert(chain, key)
        .map_err(|error| format!("the certificate and its key were refused: {error}"))?;
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(Serving {
        acceptor: TlsAcceptor::from(Arc::new(config)),
        fingerprint,
    })
}

/// The name in the certificate the server makes for itself. It stands for nothing: a member trusts that
/// certificate by its fingerprint, never by a name.
const OWN_NAME: &str = "brigade-server";

/// The certificate the server made for itself and its key, `<data>/tls/certificate.pem` and `key.pem`, and
/// whether this call made them.
///
/// On the first start neither file is there and both are made: an ECDSA P-256 key, mode 0600, and a
/// certificate signed with it. The certificate names no address and is valid for as long as its format
/// allows, because what trusts it reads neither: a team file pins its fingerprint. Every later start reads
/// the same two files, since a new certificate would be one no team file pins.
///
/// # Errors
/// A sentence for the operator. A directory that holds one of the two files and not the other is refused:
/// the server never replaces half of a pair, and never a certificate that a team may have pinned.
pub fn own_certificate(data: &Path) -> Result<(PathBuf, PathBuf, bool), String> {
    let dir = data.join("tls");
    let (certificate, key) = (dir.join("certificate.pem"), dir.join("key.pem"));
    match (certificate.exists(), key.exists()) {
        (true, true) => return Ok((certificate, key, false)),
        (false, false) => {}
        _ => {
            return Err(format!(
                "{} holds the server's certificate or its key and not both: restore the missing file, or \
                 remove the directory to have a new certificate made, which every team file must then pin anew",
                dir.display()
            ));
        }
    }
    let refused =
        |error: &dyn std::fmt::Display| format!("the server's own certificate could not be made: {error}");
    let pair = rcgen::KeyPair::generate().map_err(|error| refused(&error))?;
    let made = rcgen::CertificateParams::new(vec![OWN_NAME.to_owned()])
        .and_then(|params| params.self_signed(&pair))
        .map_err(|error| refused(&error))?;
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&dir)
        .map_err(|error| format!("{} could not be created: {error}", dir.display()))?;
    // Both are written beside their names and moved into place, the key first: a start that is cut short
    // leaves nothing, or a key with no certificate, which the next start refuses by name.
    for (path, text, mode) in [
        (&key, pair.serialize_pem(), 0o600),
        (&certificate, made.pem(), 0o644),
    ] {
        let beside = path.with_extension("pem.new");
        let _ = fs::remove_file(&beside);
        let written = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(mode)
            .open(&beside)
            .and_then(|mut file| {
                file.write_all(text.as_bytes())?;
                file.sync_all()
            })
            .and_then(|()| fs::rename(&beside, path));
        written.map_err(|error| format!("{} could not be written: {error}", path.display()))?;
    }
    Ok((certificate, key, true))
}
