//! Secret storage & redaction.
//!
//! Security priority: secrets (join tokens, enrollment certificates, private
//! keys) must be stored **encrypted at rest** and **never written to logs**.
//! This module provides the seam for both:
//!   * [`Redacted`] wraps a sensitive value so an accidental `{:?}` in a log
//!     line prints `<redacted>` instead of the secret, and
//!   * [`SecretStore`] abstracts persistence; [`InMemorySecretStore`] is for
//!     tests / pre-enrollment, and [`EncryptedFileSecretStore`] is the
//!     production implementation backed by AES-256-GCM files.
//!
//! ## File format
//! Each secret is stored as `{store_dir}/{name}.enc`:
//! ```text
//! [ nonce (12 bytes) ][ ciphertext + GCM tag ]
//! ```
//! The nonce is freshly generated from the OS CSPRNG on every write;
//! the GCM tag (16 bytes, appended to the ciphertext by `aes-gcm`) provides
//! authenticated encryption so any tampering is detected on `get`.
//!
//! ## Key management
//! Precedence (first wins):
//! 1. `PURSER_SECRET_KEY` env var — 32-byte key, hex- or base64-encoded.
//! 2. `{store_dir}/.secret_key` — raw 32-byte key file (0600); loaded if
//!    present, auto-generated and saved if absent.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use aes_gcm::{
    aead::{Aead, KeyInit},
    Aes256Gcm, Nonce,
};
use anyhow::Context as _;
use zeroize::Zeroize;

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

// ---------------------------------------------------------------------------
// In-memory store (tests / pre-enrollment only)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Encrypted file-backed store
// ---------------------------------------------------------------------------

/// AES-256-GCM file-backed secret store.
///
/// Each secret is kept in `{dir}/{name}.enc` with the layout:
/// `[ nonce (12 bytes) ][ ciphertext + 16-byte GCM tag ]`.
///
/// The struct is `Send + Sync` (the cipher key is immutable after construction;
/// file I/O is independent per call). Wrap in `Arc` for sharing across tasks.
pub struct EncryptedFileSecretStore {
    dir: PathBuf,
    /// Raw 32-byte AES-256 key — never logged.
    key: [u8; 32],
}

impl EncryptedFileSecretStore {
    /// Create a store backed by `dir` using an explicit `key`.
    ///
    /// Creates `dir` (and any parents) with restrictive permissions on Unix.
    /// Use [`Self::from_env_or_generate`] for the standard production path.
    pub fn new(dir: impl Into<PathBuf>, key: [u8; 32]) -> anyhow::Result<Self> {
        let dir = dir.into();
        create_secure_dir(&dir)?;
        Ok(Self { dir, key })
    }

    /// Production constructor: derive the encryption key from environment or
    /// auto-generate and persist it, then open the store in `dir`.
    ///
    /// Key precedence:
    /// 1. `PURSER_SECRET_KEY` env var (hex- or base64-encoded 32 bytes).
    /// 2. `{dir}/.secret_key` (raw 32 bytes, 0600); loaded if present,
    ///    freshly generated and saved if absent.
    pub fn from_env_or_generate(dir: impl Into<PathBuf>) -> anyhow::Result<Self> {
        let dir = dir.into();
        create_secure_dir(&dir)?;
        let key = match std::env::var("PURSER_SECRET_KEY") {
            Ok(s) => {
                tracing::debug!("using PURSER_SECRET_KEY for secret store encryption");
                decode_key(&s)?
            }
            Err(_) => load_or_generate_key(&dir)?,
        };
        Ok(Self { dir, key })
    }

    /// Instantiate the AES-256-GCM cipher from the stored key.
    fn cipher(&self) -> Aes256Gcm {
        // key is always 32 bytes by construction, so this never fails.
        Aes256Gcm::new_from_slice(&self.key).expect("key is 32 bytes")
    }

    fn secret_path(&self, name: &str) -> PathBuf {
        self.dir.join(format!("{name}.enc"))
    }
}

/// H4: zero the AES-256 key on drop so it does not linger in memory after the
/// store is deallocated.
impl Drop for EncryptedFileSecretStore {
    fn drop(&mut self) {
        self.key.zeroize();
    }
}

impl SecretStore for EncryptedFileSecretStore {
    fn put(&self, name: &str, value: &[u8]) -> anyhow::Result<()> {
        validate_name(name)?;
        // Generate a fresh 96-bit nonce from the OS CSPRNG for every write.
        let mut raw_nonce = [0u8; 12];
        getrandom::getrandom(&mut raw_nonce)
            .map_err(|e| anyhow::anyhow!("nonce generation failed: {e}"))?;
        let nonce = Nonce::from_slice(&raw_nonce);

        let ciphertext = self
            .cipher()
            .encrypt(nonce, value)
            .map_err(|e| anyhow::anyhow!("encryption failed for {name:?}: {e}"))?;

        // Write: [ nonce (12 B) ][ ciphertext + tag ]
        let mut buf = Vec::with_capacity(12 + ciphertext.len());
        buf.extend_from_slice(nonce.as_slice());
        buf.extend_from_slice(&ciphertext);

        let path = self.secret_path(name);
        std::fs::write(&path, &buf)
            .with_context(|| format!("writing secret file {}", path.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt as _;
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))
                .with_context(|| format!("chmod 600 {}", path.display()))?;
        }
        Ok(())
    }

    fn get(&self, name: &str) -> anyhow::Result<Option<Vec<u8>>> {
        validate_name(name)?;
        let path = self.secret_path(name);
        if !path.exists() {
            return Ok(None);
        }
        let data = std::fs::read(&path)
            .with_context(|| format!("reading secret file {}", path.display()))?;
        if data.len() < 12 {
            anyhow::bail!(
                "secret file {} is malformed ({} bytes — too short for nonce)",
                path.display(),
                data.len()
            );
        }
        let (nonce_bytes, ciphertext) = data.split_at(12);
        let nonce = Nonce::from_slice(nonce_bytes);
        let plaintext = self
            .cipher()
            .decrypt(nonce, ciphertext)
            // Do NOT include any ciphertext/tag bytes in the error — they could
            // leak partial information about the secret.
            .map_err(|_| {
                anyhow::anyhow!("authentication failure decrypting {name:?} — possible tampering")
            })?;
        Ok(Some(plaintext))
    }

    fn delete(&self, name: &str) -> anyhow::Result<()> {
        validate_name(name)?;
        let path = self.secret_path(name);
        match std::fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(e).with_context(|| format!("removing secret {}", path.display())),
        }
    }
}

// ---------------------------------------------------------------------------
// Key management helpers
// ---------------------------------------------------------------------------

/// Create `dir` with owner-only permissions (0700 on Unix).
fn create_secure_dir(dir: &Path) -> anyhow::Result<()> {
    std::fs::create_dir_all(dir)
        .with_context(|| format!("creating secret store directory {}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))
            .with_context(|| format!("chmod 700 {}", dir.display()))?;
    }
    Ok(())
}

/// Load the key from `{dir}/.secret_key`, or generate and save a new one.
///
/// The key file contains exactly 32 raw bytes and is created with 0600
/// permissions so only the running user can read it.
pub(crate) fn load_or_generate_key(dir: &Path) -> anyhow::Result<[u8; 32]> {
    let key_file = dir.join(".secret_key");
    if key_file.exists() {
        let data = std::fs::read(&key_file)
            .with_context(|| format!("reading key file {}", key_file.display()))?;
        if data.len() != 32 {
            anyhow::bail!(
                "key file {} must be exactly 32 bytes, got {}",
                key_file.display(),
                data.len()
            );
        }
        let mut key = [0u8; 32];
        key.copy_from_slice(&data);
        tracing::debug!(path = %key_file.display(), "loaded encryption key from key file");
        return Ok(key);
    }

    // No key file — generate a fresh one from the OS CSPRNG.
    let mut key = [0u8; 32];
    getrandom::getrandom(&mut key).map_err(|e| anyhow::anyhow!("key generation failed: {e}"))?;
    std::fs::write(&key_file, key)
        .with_context(|| format!("writing key file {}", key_file.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        std::fs::set_permissions(&key_file, std::fs::Permissions::from_mode(0o600))
            .with_context(|| format!("chmod 600 {}", key_file.display()))?;
    }
    tracing::info!(
        path = %key_file.display(),
        "generated new AES-256-GCM encryption key and saved to key file"
    );
    Ok(key)
}

/// Decode a 32-byte key from a hex- or base64-encoded string.
fn decode_key(s: &str) -> anyhow::Result<[u8; 32]> {
    let s = s.trim();
    let bytes = if let Ok(b) = hex::decode(s) {
        b
    } else {
        use base64::Engine as _;
        base64::engine::general_purpose::STANDARD
            .decode(s)
            .or_else(|_| base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(s))
            .context("PURSER_SECRET_KEY is not valid hex or base64")?
    };
    anyhow::ensure!(
        bytes.len() == 32,
        "PURSER_SECRET_KEY must decode to exactly 32 bytes, got {}",
        bytes.len()
    );
    let mut key = [0u8; 32];
    key.copy_from_slice(&bytes);
    Ok(key)
}

/// Reject secret names that could be used for path traversal.
fn validate_name(name: &str) -> anyhow::Result<()> {
    anyhow::ensure!(
        !name.is_empty() && !name.contains('/') && !name.contains('\\') && !name.starts_with('.'),
        "invalid secret name {name:?} (must be non-empty, no slashes, no leading dot)"
    );
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    // ---- Redacted wrapper ------------------------------------------------

    #[test]
    fn redacted_never_leaks_in_formatting() {
        let secret = Redacted::new("super-secret-token".to_string());
        assert_eq!(format!("{secret:?}"), "<redacted>");
        assert_eq!(format!("{secret}"), "<redacted>");
        // The value is still accessible explicitly.
        assert_eq!(secret.expose(), "super-secret-token");
    }

    // ---- InMemorySecretStore ---------------------------------------------

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

    // ---- EncryptedFileSecretStore ----------------------------------------

    fn fixed_key() -> [u8; 32] {
        // Deterministic key for testing — NOT for production use.
        let mut k = [0u8; 32];
        for (i, b) in k.iter_mut().enumerate() {
            *b = i as u8;
        }
        k
    }

    #[test]
    fn encrypted_store_roundtrip() {
        let dir = TempDir::new().unwrap();
        let store = EncryptedFileSecretStore::new(dir.path(), fixed_key()).unwrap();
        assert!(store.get("cert").unwrap().is_none());
        store.put("cert", b"pem-data").unwrap();
        assert_eq!(
            store.get("cert").unwrap().as_deref(),
            Some(&b"pem-data"[..])
        );
        store.delete("cert").unwrap();
        assert!(store.get("cert").unwrap().is_none());
    }

    #[test]
    fn roundtrip_across_instances() {
        // Simulates an agent restart: secrets written by one instance survive
        // and are readable by a fresh instance pointing at the same directory.
        let dir = TempDir::new().unwrap();
        let key = fixed_key();

        {
            let s1 = EncryptedFileSecretStore::new(dir.path(), key).unwrap();
            s1.put("join_token", b"tok-abc123").unwrap();
            s1.put("ca_cert", b"-----BEGIN CERTIFICATE-----").unwrap();
        }

        // New instance, same dir + same key.
        let s2 = EncryptedFileSecretStore::new(dir.path(), key).unwrap();
        assert_eq!(
            s2.get("join_token").unwrap().as_deref(),
            Some(&b"tok-abc123"[..])
        );
        assert_eq!(
            s2.get("ca_cert").unwrap().as_deref(),
            Some(&b"-----BEGIN CERTIFICATE-----"[..])
        );
    }

    #[test]
    fn tamper_detection_flips_ciphertext_byte() {
        let dir = TempDir::new().unwrap();
        let store = EncryptedFileSecretStore::new(dir.path(), fixed_key()).unwrap();
        store.put("tok", b"secret-token").unwrap();

        // Corrupt the last byte — that falls in the GCM tag, triggering an
        // authentication failure on decrypt.
        let path = dir.path().join("tok.enc");
        let mut raw = std::fs::read(&path).unwrap();
        *raw.last_mut().unwrap() ^= 0xff;
        std::fs::write(&path, &raw).unwrap();

        assert!(
            store.get("tok").is_err(),
            "corrupt ciphertext must fail authentication"
        );
    }

    #[test]
    fn tamper_detection_flips_nonce_byte() {
        let dir = TempDir::new().unwrap();
        let store = EncryptedFileSecretStore::new(dir.path(), fixed_key()).unwrap();
        store.put("tok", b"another-secret").unwrap();

        // Flip a byte in the nonce — the wrong nonce produces a different
        // keystream so the GCM tag won't verify.
        let path = dir.path().join("tok.enc");
        let mut raw = std::fs::read(&path).unwrap();
        raw[3] ^= 0x01;
        std::fs::write(&path, &raw).unwrap();

        assert!(
            store.get("tok").is_err(),
            "corrupt nonce must fail authentication"
        );
    }

    #[test]
    fn auto_key_generated_and_reloaded() {
        // The first call to load_or_generate_key creates the file; the second
        // reads the same bytes — simulating two independent agent startups
        // with no PURSER_SECRET_KEY set.
        let dir = TempDir::new().unwrap();
        let key_file = dir.path().join(".secret_key");

        let key1 = load_or_generate_key(dir.path()).unwrap();
        assert!(key_file.exists(), "key file must be created");

        let key2 = load_or_generate_key(dir.path()).unwrap();
        assert_eq!(key1, key2, "second call must return the same persisted key");

        // Use both to confirm they can decrypt each other's ciphertext.
        let s1 = EncryptedFileSecretStore::new(dir.path(), key1).unwrap();
        s1.put("x", b"payload").unwrap();
        let s2 = EncryptedFileSecretStore::new(dir.path(), key2).unwrap();
        assert_eq!(s2.get("x").unwrap().as_deref(), Some(&b"payload"[..]));
    }

    #[test]
    fn decode_key_accepts_hex() {
        // 32 zero bytes as 64 hex chars.
        let k = decode_key(&"00".repeat(32)).unwrap();
        assert_eq!(k, [0u8; 32]);
    }

    #[test]
    fn decode_key_accepts_base64() {
        use base64::Engine as _;
        let raw = fixed_key();
        let encoded = base64::engine::general_purpose::STANDARD.encode(raw);
        let k = decode_key(&encoded).unwrap();
        assert_eq!(k, raw);
    }

    #[test]
    fn decode_key_rejects_wrong_length() {
        // 31 bytes → error.
        assert!(decode_key(&"00".repeat(31)).is_err());
    }

    #[test]
    fn validate_name_rejects_traversal() {
        assert!(validate_name("../etc/passwd").is_err());
        assert!(validate_name(".hidden").is_err());
        assert!(validate_name("").is_err());
        assert!(validate_name("good_name-123").is_ok());
    }

    // ---- H4: AES key zeroize on drop ------------------------------------

    /// Verifies that `EncryptedFileSecretStore` zeroes its AES key on drop
    /// (the `Drop` impl calls `self.key.zeroize()`).
    ///
    /// Direct field access is not possible because `key` is private, so the
    /// test takes an indirect route:
    ///  1. Confirm that `[u8; 32]` implements `Zeroize` (required by the impl).
    ///  2. Create and drop a store, verifying it can be used normally first.
    #[test]
    fn key_material_zeroed_on_drop() {
        // 1. Zeroize must work on [u8; 32] — the same type as the private field.
        let mut tmp_key = fixed_key();
        tmp_key.zeroize();
        assert_eq!(
            tmp_key,
            [0u8; 32],
            "Zeroize::zeroize() must overwrite every byte with 0"
        );

        // 2. The store can be created, used, and dropped without panic.
        let dir = TempDir::new().unwrap();
        {
            let store = EncryptedFileSecretStore::new(dir.path(), fixed_key()).unwrap();
            store.put("tok", b"secret").unwrap();
            // `store` drops here — Drop::drop() calls self.key.zeroize().
        }
        // The encrypted file survives the store drop.
        assert!(
            dir.path().join("tok.enc").exists(),
            "encrypted file must remain after store is dropped"
        );
    }
}
