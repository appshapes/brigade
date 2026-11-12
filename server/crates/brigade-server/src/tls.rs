//! TLS, when the server serves it itself.
//!
//! The operator gives the server a certificate chain and its key as two PEM files. The other way to run a
//! server off loopback is `--tls-in-front`: plain HTTP behind something that ends TLS.

use std::path::Path;
use std::sync::Arc;

use rustls::ServerConfig;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use tokio_rustls::TlsAcceptor;

/// What accepts a TLS connection with the certificate chain in `certificate` and the key in `key`.
///
/// # Errors
/// A sentence for the operator. It names the file and never what the file holds.
pub fn acceptor(certificate: &Path, key: &Path) -> Result<TlsAcceptor, String> {
    let chain: Vec<CertificateDer<'static>> = CertificateDer::pem_file_iter(certificate)
        .and_then(Iterator::collect)
        .map_err(|error| {
            format!(
                "{} does not read as a certificate chain: {error}",
                certificate.display()
            )
        })?;
    if chain.is_empty() {
        return Err(format!("{} holds no certificate", certificate.display()));
    }
    let key = PrivateKeyDer::from_pem_file(key)
        .map_err(|error| format!("{} does not read as a private key: {error}", key.display()))?;
    let mut config = ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
        .with_safe_default_protocol_versions()
        .map_err(|error| format!("TLS could not be set up: {error}"))?
        .with_no_client_auth()
        .with_single_cert(chain, key)
        .map_err(|error| format!("the certificate and its key were refused: {error}"))?;
    config.alpn_protocols = vec![b"http/1.1".to_vec()];
    Ok(TlsAcceptor::from(Arc::new(config)))
}
