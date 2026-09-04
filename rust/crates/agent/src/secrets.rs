//! Secret storage & redaction.
//!
//! Security priority: secrets (join tokens, enrollment certificates, private
//! keys) must be stored **encrypted at rest** and **never written to logs**.
//! This module provides the seam for both:
//!   * [`Redacted`] wraps a sensitive value so an accidental `{:?}` in a log
//!     line prints `<redacted>` instead of the secret, and
//!   * [`SecretStore`] abstracts persistence; [`InMemorySecretStore`] is for
//!     tests / pre-enrollment, and an encrypted on-disk store is the intended
//!     production implementation.
//!
//! TODO(phase2): `EncryptedFileSecretStore` implementing [`SecretStore`] with
//! authenticated encryption (e.g. XChaCha20-Poly1305), the key sourced from the
//! OS keyring / TPM and never logged.

use std::collections::HashMap;
use std::sync::Mutex;

/// A value whose `Debug`/`Display` never reveal its contents. Use it for tokens
/// and keys so a stray log statement cannot leak them.
#[derive(Clone)]
pub struct Redacted<T>(T);

impl<T> Redacted<T> {
    /// Wrap a sensitive value.
    pub fn new(value: T) -> Self {
        Self(value)
    }

    /// Borrow the protected value (explicit — makes access auditable).
    pub fn expose(&self) -> &T {
        &self.0
    }

    /// Consume and return the protected value.
    pub fn into_inner(self) -> T {
        self.0
    }
}

impl<T> std::fmt::Debug for Redacted<T> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("<redacted>")
    }
}

impl<T> std::fmt::Display for Redacted<T> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("<redacted>")
    }
}

impl<T> From<T> for Redacted<T> {
    fn from(value: T) -> Self {
        Self(value)
    }
}

/// Persistence for node secrets. Implementations must encrypt at rest and must
/// not log values.
pub trait SecretStore: Send + Sync {
    /// Store `value` under `key`, overwriting any existing entry.
    fn put(&self, key: &str, value: &[u8]) -> anyhow::Result<()>;
    /// Retrieve the value stored under `key`, if any.
    fn get(&self, key: &str) -> anyhow::Result<Option<Vec<u8>>>;
    /// Remove `key` if present.
    fn delete(&self, key: &str) -> anyhow::Result<()>;
}

/// Volatile, process-local secret store for tests and pre-enrollment. Provides
/// no at-rest protection — never use it to persist real secrets.
#[derive(Default)]
pub struct InMemorySecretStore {
    inner: Mutex<HashMap<String, Vec<u8>>>,
}

impl InMemorySecretStore {
    /// A new, empty store.
    pub fn new() -> Self {
        Self::default()
    }
}

impl SecretStore for InMemorySecretStore {
    fn put(&self, key: &str, value: &[u8]) -> anyhow::Result<()> {
        self.inner
            .lock()
            .unwrap()
            .insert(key.to_string(), value.to_vec());
        Ok(())
    }

    fn get(&self, key: &str) -> anyhow::Result<Option<Vec<u8>>> {
        Ok(self.inner.lock().unwrap().get(key).cloned())
    }

    fn delete(&self, key: &str) -> anyhow::Result<()> {
        self.inner.lock().unwrap().remove(key);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn redacted_never_leaks_in_formatting() {
        let secret = Redacted::new("super-secret-token".to_string());
        assert_eq!(format!("{secret:?}"), "<redacted>");
        assert_eq!(format!("{secret}"), "<redacted>");
        // The value is still accessible explicitly.
        assert_eq!(secret.expose(), "super-secret-token");
    }

    #[test]
    fn in_memory_store_roundtrips() {
        let store = InMemorySecretStore::new();
        assert!(store.get("cert").unwrap().is_none());
        store.put("cert", b"pem-bytes").unwrap();
        assert_eq!(
            store.get("cert").unwrap().as_deref(),
            Some(&b"pem-bytes"[..])
        );
        store.delete("cert").unwrap();
        assert!(store.get("cert").unwrap().is_none());
    }
}
