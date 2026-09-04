//! Local model weight cache.
//!
//! Manages on-disk model artifacts on the node:
//!   * **fetch** from a configured mirror (via a pluggable [`Fetcher`]),
//!   * **verify** a cryptographic checksum (SHA-256) before a blob is admitted,
//!   * **content-addressed storage** so identical weights are stored once
//!     (conceptual dedup: two model refs with the same checksum share a blob),
//!   * **LRU eviction** under a configurable disk budget, with pinning so the
//!     model an engine is actively serving is never evicted.
//!
//! The supervisor consults this cache before starting an engine; the probe's
//! `disk_free_gb` bounds what may be cached.
//!
//! The default [`FileMirrorFetcher`] copies from a rack-local mirror directory
//! (NFS / mounted object store / `file://` URL) — realistic and dependency-free.
//! Remote HTTP(S) mirrors are a drop-in [`Fetcher`] implementation.
//! TODO(phase2): ship an `HttpFetcher` (behind an optional `http-fetch` feature
//! using a minimal client) for pulling from an internet origin.
//!
//! This subsystem is fully unit-tested; the supervisor will consult it on the
//! engine-load path once a real (non-mock) engine adapter that loads on-disk
//! weights is wired in.

use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;

use async_trait::async_trait;
use sha2::{Digest, Sha256};
use tokio::io::AsyncReadExt;

/// A model artifact to fetch and cache.
#[derive(Clone, Debug)]
pub struct ModelArtifact {
    /// Logical cache key (e.g. `"qwen3-30b:Q4_K_M"`).
    pub model_ref: String,
    /// Where to fetch the weights from (mirror-relative path, absolute path, or
    /// `file://` URL for the default fetcher).
    pub url: String,
    /// Expected lowercase-hex SHA-256 of the artifact. Admission is refused on
    /// mismatch.
    pub sha256: String,
}

/// Errors surfaced by the cache.
#[derive(Debug)]
pub enum CacheError {
    /// The fetched artifact's checksum did not match the expected value.
    ChecksumMismatch { expected: String, actual: String },
    /// The requested artifact was too large to ever fit the budget.
    TooLarge { size: u64, budget: u64 },
    /// An underlying fetch / IO failure.
    Io(String),
}

impl std::fmt::Display for CacheError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CacheError::ChecksumMismatch { expected, actual } => write!(
                f,
                "checksum mismatch: expected {expected}, got {actual}"
            ),
            CacheError::TooLarge { size, budget } => {
                write!(f, "artifact ({size} bytes) exceeds cache budget ({budget} bytes)")
            }
            CacheError::Io(e) => write!(f, "cache io error: {e}"),
        }
    }
}

impl std::error::Error for CacheError {}

/// Fetches an artifact from `url` into the local file `dest`.
#[async_trait]
pub trait Fetcher: Send + Sync {
    /// Copy/download the artifact at `url` to `dest`. Implementations must write
    /// the complete artifact (or return `Err` without leaving a partial file the
    /// cache will admit — the cache always re-hashes before admission).
    async fn fetch(&self, url: &str, dest: &Path) -> anyhow::Result<()>;
}

/// Copies artifacts from a local/mounted mirror. Resolves `url` as:
///   * `file:///abs/path` or `file://relative` → that path,
///   * an absolute path → used directly,
///   * a relative path → joined onto `mirror_root` (if configured).
#[derive(Clone, Debug, Default)]
pub struct FileMirrorFetcher {
    /// Base directory for relative URLs.
    pub mirror_root: Option<PathBuf>,
}

impl FileMirrorFetcher {
    /// A fetcher rooted at `mirror_root` for relative URLs.
    pub fn new(mirror_root: impl Into<PathBuf>) -> Self {
        Self {
            mirror_root: Some(mirror_root.into()),
        }
    }

    fn resolve(&self, url: &str) -> PathBuf {
        let raw = url.strip_prefix("file://").unwrap_or(url);
        let path = Path::new(raw);
        if path.is_absolute() {
            path.to_path_buf()
        } else if let Some(root) = &self.mirror_root {
            root.join(path)
        } else {
            path.to_path_buf()
        }
    }
}

#[async_trait]
impl Fetcher for FileMirrorFetcher {
    async fn fetch(&self, url: &str, dest: &Path) -> anyhow::Result<()> {
        let src = self.resolve(url);
        tokio::fs::copy(&src, dest)
            .await
            .map_err(|e| anyhow::anyhow!("copy {} -> {}: {e}", src.display(), dest.display()))?;
        Ok(())
    }
}

/// Bookkeeping for one cached logical model.
#[derive(Clone, Debug)]
struct Entry {
    /// Content address (lowercase-hex sha256); the blob lives at `blobs/<sha>`.
    sha256: String,
    /// Size in bytes.
    size: u64,
    /// Logical clock value of last access (higher = more recently used).
    last_access: u64,
}

/// On-disk, checksum-verified, LRU-evicting model cache.
pub struct ModelCache {
    root: PathBuf,
    max_bytes: u64,
    fetcher: Box<dyn Fetcher>,
    clock: AtomicU64,
    inner: Mutex<Inner>,
}

#[derive(Default)]
struct Inner {
    /// model_ref -> entry.
    entries: HashMap<String, Entry>,
    /// Pinned model_refs, exempt from eviction.
    pinned: HashSet<String>,
}

impl ModelCache {
    /// Open (creating if needed) a cache at `root` with a `max_bytes` budget,
    /// using `fetcher` to bring in missing artifacts.
    pub async fn open(
        root: impl Into<PathBuf>,
        max_bytes: u64,
        fetcher: Box<dyn Fetcher>,
    ) -> anyhow::Result<Self> {
        let root = root.into();
        tokio::fs::create_dir_all(root.join("blobs")).await?;
        tokio::fs::create_dir_all(root.join("tmp")).await?;
        Ok(Self {
            root,
            max_bytes,
            fetcher,
            clock: AtomicU64::new(1),
            inner: Mutex::new(Inner::default()),
        })
    }

    /// Path to the blob for a content address.
    fn blob_path(&self, sha: &str) -> PathBuf {
        self.root.join("blobs").join(sha)
    }

    /// Ensure `artifact` is present and verified, returning the on-disk path.
    ///
    /// If already cached (and the blob still exists) this only touches the LRU
    /// clock. Otherwise it fetches to a temp file, verifies the SHA-256, admits
    /// the blob (deduplicating against an identical existing blob), and evicts
    /// LRU entries until the budget is honoured.
    pub async fn get_or_fetch(&self, artifact: &ModelArtifact) -> anyhow::Result<PathBuf> {
        // Fast path: already cached and present on disk.
        if let Some(sha) = self.touch_if_present(&artifact.model_ref) {
            return Ok(self.blob_path(&sha));
        }

        // Fetch to a unique temp file.
        let tmp = self.root.join("tmp").join(format!(
            "{}.{}",
            sanitize(&artifact.model_ref),
            self.clock.fetch_add(1, Ordering::SeqCst)
        ));
        self.fetcher.fetch(&artifact.url, &tmp).await?;

        // Verify checksum before admitting anything.
        let actual = match sha256_file(&tmp).await {
            Ok(s) => s,
            Err(e) => {
                let _ = tokio::fs::remove_file(&tmp).await;
                return Err(anyhow::anyhow!(CacheError::Io(e.to_string())));
            }
        };
        if actual != artifact.sha256.to_lowercase() {
            let _ = tokio::fs::remove_file(&tmp).await;
            return Err(anyhow::anyhow!(CacheError::ChecksumMismatch {
                expected: artifact.sha256.to_lowercase(),
                actual,
            }));
        }

        let size = tokio::fs::metadata(&tmp).await?.len();
        if size > self.max_bytes {
            let _ = tokio::fs::remove_file(&tmp).await;
            return Err(anyhow::anyhow!(CacheError::TooLarge {
                size,
                budget: self.max_bytes,
            }));
        }

        // Admit: content-address the blob. If an identical blob already exists
        // (dedup), drop the temp; otherwise move the temp into place.
        let blob = self.blob_path(&actual);
        if tokio::fs::try_exists(&blob).await.unwrap_or(false) {
            let _ = tokio::fs::remove_file(&tmp).await;
        } else {
            // rename is atomic within a filesystem; fall back to copy across FS.
            if tokio::fs::rename(&tmp, &blob).await.is_err() {
                tokio::fs::copy(&tmp, &blob).await?;
                let _ = tokio::fs::remove_file(&tmp).await;
            }
        }

        let access = self.clock.fetch_add(1, Ordering::SeqCst);
        {
            let mut inner = self.inner.lock().unwrap();
            inner.entries.insert(
                artifact.model_ref.clone(),
                Entry {
                    sha256: actual.clone(),
                    size,
                    last_access: access,
                },
            );
        }

        self.evict_to_budget().await;
        Ok(self.blob_path(&actual))
    }

    /// Pin `model_ref` so it is never evicted (e.g. the running model).
    pub fn pin(&self, model_ref: &str) {
        self.inner.lock().unwrap().pinned.insert(model_ref.to_string());
    }

    /// Remove a pin.
    pub fn unpin(&self, model_ref: &str) {
        self.inner.lock().unwrap().pinned.remove(model_ref);
    }

    /// Total bytes currently accounted in the cache.
    pub fn total_bytes(&self) -> u64 {
        self.inner.lock().unwrap().entries.values().map(|e| e.size).sum()
    }

    /// Logical model refs currently cached (sorted).
    pub fn cached_refs(&self) -> Vec<String> {
        let mut refs: Vec<String> = self.inner.lock().unwrap().entries.keys().cloned().collect();
        refs.sort();
        refs
    }

    /// Whether `model_ref` is cached.
    pub fn contains(&self, model_ref: &str) -> bool {
        self.inner.lock().unwrap().entries.contains_key(model_ref)
    }

    /// Touch the LRU clock for `model_ref` if cached and its blob still exists;
    /// returns the content address on success.
    fn touch_if_present(&self, model_ref: &str) -> Option<String> {
        let mut inner = self.inner.lock().unwrap();
        let entry = inner.entries.get(model_ref)?;
        let sha = entry.sha256.clone();
        if !self.blob_path(&sha).exists() {
            // Blob vanished under us; drop the stale index entry.
            inner.entries.remove(model_ref);
            return None;
        }
        let access = self.clock.fetch_add(1, Ordering::SeqCst);
        if let Some(e) = inner.entries.get_mut(model_ref) {
            e.last_access = access;
        }
        Some(sha)
    }

    /// Evict least-recently-used, unpinned entries until under budget. A blob is
    /// deleted only once no remaining entry references it (dedup-aware).
    async fn evict_to_budget(&self) {
        loop {
            let victim = {
                let inner = self.inner.lock().unwrap();
                let total: u64 = inner.entries.values().map(|e| e.size).sum();
                if total <= self.max_bytes {
                    return;
                }
                // Pick the unpinned entry with the smallest last_access.
                inner
                    .entries
                    .iter()
                    .filter(|(k, _)| !inner.pinned.contains(*k))
                    .min_by_key(|(_, e)| e.last_access)
                    .map(|(k, e)| (k.clone(), e.sha256.clone()))
            };

            let Some((model_ref, sha)) = victim else {
                // Everything over budget is pinned; nothing more we can do.
                tracing::warn!(
                    total = self.total_bytes(),
                    budget = self.max_bytes,
                    "cache over budget but all remaining entries are pinned"
                );
                return;
            };

            let delete_blob = {
                let mut inner = self.inner.lock().unwrap();
                inner.entries.remove(&model_ref);
                // Delete the blob only if no other entry still references it.
                !inner.entries.values().any(|e| e.sha256 == sha)
            };
            if delete_blob {
                let _ = tokio::fs::remove_file(self.blob_path(&sha)).await;
            }
            tracing::debug!(%model_ref, "evicted model from cache (LRU)");
        }
    }
}

/// Stream a file through SHA-256, returning the lowercase-hex digest.
pub async fn sha256_file(path: &Path) -> std::io::Result<String> {
    let mut file = tokio::fs::File::open(path).await?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; 64 * 1024];
    loop {
        let n = file.read(&mut buf).await?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hex::encode(&hasher.finalize()[..]))
}

/// SHA-256 of an in-memory byte slice, lowercase hex.
pub fn sha256_bytes(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hex::encode(&hasher.finalize()[..])
}

/// Make a model ref safe to use as a temp-file name component.
fn sanitize(model_ref: &str) -> String {
    model_ref
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '_' })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use tempfile::tempdir;

    /// Write `content` to `dir/name` and return (path, sha256-hex).
    async fn write_mirror_file(dir: &Path, name: &str, content: &[u8]) -> (PathBuf, String) {
        let path = dir.join(name);
        tokio::fs::write(&path, content).await.unwrap();
        (path, sha256_bytes(content))
    }

    #[test]
    fn sha256_of_known_input() {
        // echo -n "abc" | sha256sum
        assert_eq!(
            sha256_bytes(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }

    #[tokio::test]
    async fn fetch_verifies_and_caches() {
        let mirror = tempdir().unwrap();
        let cache_dir = tempdir().unwrap();
        let (_p, sha) = write_mirror_file(mirror.path(), "weights.bin", b"hello weights").await;

        let cache = ModelCache::open(
            cache_dir.path(),
            1_000_000,
            Box::new(FileMirrorFetcher::new(mirror.path())),
        )
        .await
        .unwrap();

        let art = ModelArtifact {
            model_ref: "m1".into(),
            url: "weights.bin".into(),
            sha256: sha.clone(),
        };
        let path = cache.get_or_fetch(&art).await.unwrap();
        assert!(path.exists());
        assert!(cache.contains("m1"));
        assert_eq!(cache.total_bytes(), b"hello weights".len() as u64);

        // Second fetch is a cache hit (path unchanged, still present).
        let path2 = cache.get_or_fetch(&art).await.unwrap();
        assert_eq!(path, path2);
    }

    #[tokio::test]
    async fn checksum_mismatch_is_rejected() {
        let mirror = tempdir().unwrap();
        let cache_dir = tempdir().unwrap();
        write_mirror_file(mirror.path(), "weights.bin", b"real content").await;

        let cache = ModelCache::open(
            cache_dir.path(),
            1_000_000,
            Box::new(FileMirrorFetcher::new(mirror.path())),
        )
        .await
        .unwrap();

        let art = ModelArtifact {
            model_ref: "m1".into(),
            url: "weights.bin".into(),
            sha256: sha256_bytes(b"WRONG content"), // deliberately wrong
        };
        let err = cache.get_or_fetch(&art).await.unwrap_err();
        assert!(
            err.to_string().contains("checksum mismatch"),
            "unexpected error: {err}"
        );
        assert!(!cache.contains("m1"));
        assert_eq!(cache.total_bytes(), 0);
    }

    #[tokio::test]
    async fn lru_eviction_under_budget() {
        let mirror = tempdir().unwrap();
        let cache_dir = tempdir().unwrap();
        // Three ~100-byte artifacts, budget fits ~two of them.
        let big = vec![0u8; 100];
        let a = {
            let c = [big.as_slice(), b"A"].concat();
            write_mirror_file(mirror.path(), "a.bin", &c).await
        };
        let b = {
            let c = [big.as_slice(), b"B"].concat();
            write_mirror_file(mirror.path(), "b.bin", &c).await
        };
        let c = {
            let c = [big.as_slice(), b"C"].concat();
            write_mirror_file(mirror.path(), "c.bin", &c).await
        };

        let cache = ModelCache::open(
            cache_dir.path(),
            230, // fits two 101-byte blobs but not three
            Box::new(FileMirrorFetcher::new(mirror.path())),
        )
        .await
        .unwrap();

        cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "a".into(),
                url: "a.bin".into(),
                sha256: a.1,
            })
            .await
            .unwrap();
        cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "b".into(),
                url: "b.bin".into(),
                sha256: b.1,
            })
            .await
            .unwrap();
        // Touch "a" so "b" becomes the least-recently-used.
        cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "a".into(),
                url: "a.bin".into(),
                sha256: sha256_bytes(&[big.as_slice(), b"A"].concat()),
            })
            .await
            .unwrap();
        // Adding "c" must evict the LRU victim ("b").
        cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "c".into(),
                url: "c.bin".into(),
                sha256: c.1,
            })
            .await
            .unwrap();

        assert!(cache.contains("a"), "recently used 'a' must survive");
        assert!(cache.contains("c"), "newest 'c' must be present");
        assert!(!cache.contains("b"), "LRU 'b' must have been evicted");
        assert!(cache.total_bytes() <= 230);
    }

    #[tokio::test]
    async fn pinning_prevents_eviction() {
        let mirror = tempdir().unwrap();
        let cache_dir = tempdir().unwrap();
        let big = vec![7u8; 100];
        let a = {
            let c = [big.as_slice(), b"A"].concat();
            write_mirror_file(mirror.path(), "a.bin", &c).await
        };
        let b = {
            let c = [big.as_slice(), b"B"].concat();
            write_mirror_file(mirror.path(), "b.bin", &c).await
        };

        let cache = ModelCache::open(
            cache_dir.path(),
            150, // only room for one
            Box::new(FileMirrorFetcher::new(mirror.path())),
        )
        .await
        .unwrap();

        cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "a".into(),
                url: "a.bin".into(),
                sha256: a.1,
            })
            .await
            .unwrap();
        cache.pin("a"); // never evict 'a'
        cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "b".into(),
                url: "b.bin".into(),
                sha256: b.1,
            })
            .await
            .unwrap();

        // 'a' is pinned so it survives even though it's the oldest; 'b' can't be
        // admitted-and-kept within budget, so it is the eviction victim.
        assert!(cache.contains("a"), "pinned 'a' must survive");
    }

    #[tokio::test]
    async fn dedup_shares_one_blob() {
        let mirror = tempdir().unwrap();
        let cache_dir = tempdir().unwrap();
        let (_p, sha) = write_mirror_file(mirror.path(), "shared.bin", b"identical bytes").await;

        let cache = ModelCache::open(
            cache_dir.path(),
            1_000_000,
            Box::new(FileMirrorFetcher::new(mirror.path())),
        )
        .await
        .unwrap();

        let p1 = cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "ref-one".into(),
                url: "shared.bin".into(),
                sha256: sha.clone(),
            })
            .await
            .unwrap();
        let p2 = cache
            .get_or_fetch(&ModelArtifact {
                model_ref: "ref-two".into(),
                url: "shared.bin".into(),
                sha256: sha.clone(),
            })
            .await
            .unwrap();

        // Same content address => same blob path (deduplicated on disk).
        assert_eq!(p1, p2);
        assert!(cache.contains("ref-one") && cache.contains("ref-two"));
    }

    // Keep `Arc` import used even if a future refactor drops it.
    #[allow(dead_code)]
    fn _assert_send_sync() {
        fn is_send_sync<T: Send + Sync>() {}
        is_send_sync::<Arc<ModelCache>>();
    }
}
