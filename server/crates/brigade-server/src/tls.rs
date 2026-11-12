//! TLS, when the server serves it itself.
//!
//! The operator gives the server a certificate chain and its key as two PEM files. The other way to run a
//! server off loopback is `--tls-in-front`: plain HTTP behind something that ends TLS.
//!
//! A certificate no authority signed is trusted by its fingerprint: a team file names it, and a member's
//! adapter then accepts that certificate and no other. So the server says the fingerprint when it starts.

use std::path::Path;
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
