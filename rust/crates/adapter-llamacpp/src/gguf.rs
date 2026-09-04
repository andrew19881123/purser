//! Minimal, dependency-free reader for the GGUF metadata header.
//!
//! GGUF (the container llama.cpp loads) begins with a self-describing header:
//! a magic, a version, tensor/KV counts, and a flat list of typed key/value
//! metadata entries. Purser reads this header for *discovery* — architecture,
//! layer count, context length, quantization, MoE-ness — without loading the
//! (multi-gigabyte) tensor payload.
//!
//! The reader works over any [`Read`] + [`Seek`] source, so it can run against a
//! real file *or* an in-memory synthetic buffer in tests. It **seeks past** array
//! bodies (e.g. huge tokenizer vocab arrays) instead of loading them, keeping
//! memory bounded, and it never panics on malformed input — every failure is a
//! typed [`GgufError`].
//!
//! Reference: <https://github.com/ggml-org/ggml/blob/master/docs/gguf.md>
//! (little-endian; versions 2 and 3 share this layout).

use std::collections::BTreeMap;
use std::io::{Read, Seek, SeekFrom};
use std::path::Path;

use thiserror::Error;

/// Magic at the start of every GGUF file: the ASCII bytes `GGUF`.
pub const GGUF_MAGIC: [u8; 4] = *b"GGUF";

/// Upper bound on a single string length we will allocate (guards against
/// corrupt/hostile length prefixes). 64 MiB is far above any real metadata key
/// or string value.
const MAX_STRING_LEN: u64 = 64 * 1024 * 1024;

/// Upper bound on the KV / tensor counts we will iterate (corruption guard).
const MAX_COUNT: u64 = 16 * 1024 * 1024;

/// GGUF metadata value-type discriminants (little-endian `u32`).
mod vtype {
    pub const UINT8: u32 = 0;
    pub const INT8: u32 = 1;
    pub const UINT16: u32 = 2;
    pub const INT16: u32 = 3;
    pub const UINT32: u32 = 4;
    pub const INT32: u32 = 5;
    pub const FLOAT32: u32 = 6;
    pub const BOOL: u32 = 7;
    pub const STRING: u32 = 8;
    pub const ARRAY: u32 = 9;
    pub const UINT64: u32 = 10;
    pub const INT64: u32 = 11;
    pub const FLOAT64: u32 = 12;
}

/// Failure modes of the GGUF reader.
#[derive(Debug, Error)]
pub enum GgufError {
    /// Underlying I/O failure (short read, seek error, ...).
    #[error("gguf io error: {0}")]
    Io(#[from] std::io::Error),
    /// The file does not begin with the GGUF magic.
    #[error("not a GGUF file (bad magic)")]
    BadMagic,
    /// The GGUF version is not supported (only v2/v3 are).
    #[error("unsupported GGUF version {0} (supported: >= 2)")]
    UnsupportedVersion(u32),
    /// The header is structurally invalid.
    #[error("malformed GGUF: {0}")]
    Malformed(String),
}

/// A single decoded metadata value. Arrays are stored as a *summary* (element
/// type + length) rather than their contents, so large token arrays cost O(1).
#[derive(Clone, Debug, PartialEq)]
pub enum GgufValue {
    U8(u8),
    I8(i8),
    U16(u16),
    I16(i16),
    U32(u32),
    I32(i32),
    U64(u64),
    I64(i64),
    F32(f32),
    F64(f64),
    Bool(bool),
    String(String),
    /// An array's element type and length (contents skipped).
    Array { elem_type: u32, len: u64 },
}

impl GgufValue {
    /// Best-effort interpretation as `u32` across the integer types.
    pub fn as_u32(&self) -> Option<u32> {
        match *self {
            GgufValue::U8(v) => Some(v as u32),
            GgufValue::U16(v) => Some(v as u32),
            GgufValue::U32(v) => Some(v),
            GgufValue::U64(v) => u32::try_from(v).ok(),
            GgufValue::I8(v) if v >= 0 => Some(v as u32),
            GgufValue::I16(v) if v >= 0 => Some(v as u32),
            GgufValue::I32(v) if v >= 0 => Some(v as u32),
            GgufValue::I64(v) if v >= 0 => u32::try_from(v).ok(),
            _ => None,
        }
    }

    /// Interpretation as string, if this is a string value.
    pub fn as_str(&self) -> Option<&str> {
        match self {
            GgufValue::String(s) => Some(s.as_str()),
            _ => None,
        }
    }
}

/// The decoded metadata header.
#[derive(Clone, Debug, Default)]
pub struct GgufMetadata {
    /// GGUF format version (2 or 3).
    pub version: u32,
    /// Number of tensors declared (payload not read).
    pub tensor_count: u64,
    /// All metadata key/value pairs, in a stable ordered map.
    pub kv: BTreeMap<String, GgufValue>,
}

impl GgufMetadata {
    /// `general.architecture`, e.g. `"llama"`, `"qwen2"`, `"deepseek2"`.
    pub fn architecture(&self) -> Option<&str> {
        self.kv.get("general.architecture").and_then(|v| v.as_str())
    }

    /// `general.name`.
    pub fn name(&self) -> Option<&str> {
        self.kv.get("general.name").and_then(|v| v.as_str())
    }

    /// Look up an architecture-scoped key (`<arch>.<suffix>`).
    fn arch_u32(&self, suffix: &str) -> Option<u32> {
        let arch = self.architecture()?;
        self.kv.get(&format!("{arch}.{suffix}")).and_then(|v| v.as_u32())
    }

    /// Number of transformer blocks (`<arch>.block_count`) — the layer count.
    pub fn layer_count(&self) -> Option<u32> {
        self.arch_u32("block_count")
    }

    /// Training context length (`<arch>.context_length`).
    pub fn context_length(&self) -> Option<u32> {
        self.arch_u32("context_length")
    }

    /// Embedding dimension (`<arch>.embedding_length`).
    pub fn embedding_length(&self) -> Option<u32> {
        self.arch_u32("embedding_length")
    }

    /// Attention head count (`<arch>.attention.head_count`).
    pub fn head_count(&self) -> Option<u32> {
        self.arch_u32("attention.head_count")
    }

    /// KV head count (`<arch>.attention.head_count_kv`).
    pub fn head_count_kv(&self) -> Option<u32> {
        self.arch_u32("attention.head_count_kv")
    }

    /// Number of experts (`<arch>.expert_count`); `> 0` implies a MoE model.
    pub fn expert_count(&self) -> Option<u32> {
        self.arch_u32("expert_count")
    }

    /// Whether the model is mixture-of-experts.
    pub fn is_moe(&self) -> bool {
        self.expert_count().map(|c| c > 0).unwrap_or(false)
    }

    /// `general.file_type` enum discriminant, if present.
    pub fn file_type(&self) -> Option<u32> {
        self.kv.get("general.file_type").and_then(|v| v.as_u32())
    }

    /// Human-readable quantization name derived from `general.file_type`.
    pub fn quantization(&self) -> Option<String> {
        self.file_type().map(file_type_name)
    }

    /// Flatten the useful fields into a discovery summary for the planner.
    pub fn discovery(&self) -> GgufDiscovery {
        GgufDiscovery {
            version: self.version,
            architecture: self.architecture().map(str::to_string),
            name: self.name().map(str::to_string),
            layers: self.layer_count(),
            context_length: self.context_length(),
            embedding_length: self.embedding_length(),
            head_count: self.head_count(),
            head_count_kv: self.head_count_kv(),
            expert_count: self.expert_count(),
            is_moe: self.is_moe(),
            file_type: self.file_type(),
            quantization: self.quantization(),
        }
    }
}

/// Discovery summary consumed by the planner.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct GgufDiscovery {
    pub version: u32,
    pub architecture: Option<String>,
    pub name: Option<String>,
    pub layers: Option<u32>,
    pub context_length: Option<u32>,
    pub embedding_length: Option<u32>,
    pub head_count: Option<u32>,
    pub head_count_kv: Option<u32>,
    pub expert_count: Option<u32>,
    pub is_moe: bool,
    pub file_type: Option<u32>,
    pub quantization: Option<String>,
}

/// Map a `general.file_type` (llama `ftype`) discriminant to a quant name.
/// Unknown values produce `FTYPE_<n>` so nothing is silently lost.
pub fn file_type_name(ft: u32) -> String {
    let name = match ft {
        0 => "F32",
        1 => "F16",
        2 => "Q4_0",
        3 => "Q4_1",
        7 => "Q8_0",
        8 => "Q5_0",
        9 => "Q5_1",
        10 => "Q2_K",
        11 => "Q3_K_S",
        12 => "Q3_K_M",
        13 => "Q3_K_L",
        14 => "Q4_K_S",
        15 => "Q4_K_M",
        16 => "Q5_K_S",
        17 => "Q5_K_M",
        18 => "Q6_K",
        19 => "IQ2_XXS",
        20 => "IQ2_XS",
        21 => "Q2_K_S",
        22 => "IQ3_XS",
        23 => "IQ3_XXS",
        24 => "IQ1_S",
        25 => "IQ4_NL",
        26 => "IQ3_S",
        27 => "IQ3_M",
        28 => "IQ2_S",
        29 => "IQ2_M",
        30 => "IQ4_XS",
        31 => "IQ1_M",
        32 => "BF16",
        _ => return format!("FTYPE_{ft}"),
    };
    name.to_string()
}

/// Read GGUF metadata from a `.gguf` file path.
pub fn read_metadata_from_file<P: AsRef<Path>>(path: P) -> Result<GgufMetadata, GgufError> {
    let file = std::fs::File::open(path)?;
    let mut reader = std::io::BufReader::new(file);
    read_metadata(&mut reader)
}

/// Read GGUF metadata from any seekable byte source.
pub fn read_metadata<R: Read + Seek>(r: &mut R) -> Result<GgufMetadata, GgufError> {
    let mut magic = [0u8; 4];
    r.read_exact(&mut magic)?;
    if magic != GGUF_MAGIC {
        return Err(GgufError::BadMagic);
    }

    let version = read_u32(r)?;
    if version < 2 {
        // v1 used a different (u32-count) layout and is effectively extinct.
        return Err(GgufError::UnsupportedVersion(version));
    }

    let tensor_count = read_u64(r)?;
    let kv_count = read_u64(r)?;
    if tensor_count > MAX_COUNT || kv_count > MAX_COUNT {
        return Err(GgufError::Malformed(format!(
            "implausible counts (tensors={tensor_count}, kv={kv_count})"
        )));
    }

    let mut kv = BTreeMap::new();
    for _ in 0..kv_count {
        let key = read_string(r)?;
        let value_type = read_u32(r)?;
        let value = read_value(r, value_type)?;
        kv.insert(key, value);
    }

    Ok(GgufMetadata {
        version,
        tensor_count,
        kv,
    })
}

fn read_value<R: Read + Seek>(r: &mut R, value_type: u32) -> Result<GgufValue, GgufError> {
    Ok(match value_type {
        vtype::UINT8 => GgufValue::U8(read_u8(r)?),
        vtype::INT8 => GgufValue::I8(read_u8(r)? as i8),
        vtype::UINT16 => GgufValue::U16(read_u16(r)?),
        vtype::INT16 => GgufValue::I16(read_u16(r)? as i16),
        vtype::UINT32 => GgufValue::U32(read_u32(r)?),
        vtype::INT32 => GgufValue::I32(read_u32(r)? as i32),
        vtype::FLOAT32 => GgufValue::F32(f32::from_bits(read_u32(r)?)),
        vtype::BOOL => GgufValue::Bool(read_u8(r)? != 0),
        vtype::STRING => GgufValue::String(read_string(r)?),
        vtype::UINT64 => GgufValue::U64(read_u64(r)?),
        vtype::INT64 => GgufValue::I64(read_u64(r)? as i64),
        vtype::FLOAT64 => GgufValue::F64(f64::from_bits(read_u64(r)?)),
        vtype::ARRAY => read_array(r)?,
        other => {
            return Err(GgufError::Malformed(format!(
                "unknown metadata value type {other}"
            )))
        }
    })
}

/// Read an array header, then *skip* its body (contents are not needed for
/// discovery). Returns a summary value.
fn read_array<R: Read + Seek>(r: &mut R) -> Result<GgufValue, GgufError> {
    let elem_type = read_u32(r)?;
    let len = read_u64(r)?;
    if len > MAX_COUNT {
        return Err(GgufError::Malformed(format!("implausible array length {len}")));
    }

    match scalar_size(elem_type) {
        Some(size) => {
            // Fixed-size elements: skip in one seek.
            let bytes = (len as i64).checked_mul(size as i64).ok_or_else(|| {
                GgufError::Malformed("array byte length overflow".to_string())
            })?;
            r.seek(SeekFrom::Current(bytes))?;
        }
        None if elem_type == vtype::STRING => {
            // Variable-size string elements: skip each in turn without allocating.
            for _ in 0..len {
                let slen = read_u64(r)?;
                if slen > MAX_STRING_LEN {
                    return Err(GgufError::Malformed(format!(
                        "array string element too long ({slen} bytes)"
                    )));
                }
                r.seek(SeekFrom::Current(slen as i64))?;
            }
        }
        None => {
            // Nested arrays are not defined by GGUF; refuse rather than guess.
            return Err(GgufError::Malformed(format!(
                "unsupported array element type {elem_type}"
            )));
        }
    }

    Ok(GgufValue::Array { elem_type, len })
}

/// Byte size of a fixed-size scalar element type, or `None` for variable/complex.
fn scalar_size(elem_type: u32) -> Option<u64> {
    match elem_type {
        vtype::UINT8 | vtype::INT8 | vtype::BOOL => Some(1),
        vtype::UINT16 | vtype::INT16 => Some(2),
        vtype::UINT32 | vtype::INT32 | vtype::FLOAT32 => Some(4),
        vtype::UINT64 | vtype::INT64 | vtype::FLOAT64 => Some(8),
        _ => None,
    }
}

fn read_string<R: Read>(r: &mut R) -> Result<String, GgufError> {
    let len = read_u64(r)?;
    if len > MAX_STRING_LEN {
        return Err(GgufError::Malformed(format!("string too long ({len} bytes)")));
    }
    let mut buf = vec![0u8; len as usize];
    r.read_exact(&mut buf)?;
    // Tolerate non-UTF8 rather than failing the whole parse.
    Ok(String::from_utf8_lossy(&buf).into_owned())
}

fn read_u8<R: Read>(r: &mut R) -> Result<u8, GgufError> {
    let mut b = [0u8; 1];
    r.read_exact(&mut b)?;
    Ok(b[0])
}

fn read_u16<R: Read>(r: &mut R) -> Result<u16, GgufError> {
    let mut b = [0u8; 2];
    r.read_exact(&mut b)?;
    Ok(u16::from_le_bytes(b))
}

fn read_u32<R: Read>(r: &mut R) -> Result<u32, GgufError> {
    let mut b = [0u8; 4];
    r.read_exact(&mut b)?;
    Ok(u32::from_le_bytes(b))
}

fn read_u64<R: Read>(r: &mut R) -> Result<u64, GgufError> {
    let mut b = [0u8; 8];
    r.read_exact(&mut b)?;
    Ok(u64::from_le_bytes(b))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Cursor, Write};

    /// Minimal GGUF writer for synthetic test fixtures (v3, little-endian).
    struct GgufWriter {
        buf: Vec<u8>,
        kv_count: u64,
    }

    impl GgufWriter {
        fn new() -> Self {
            Self {
                buf: Vec::new(),
                kv_count: 0,
            }
        }

        fn str(&mut self, s: &str) {
            self.buf.extend_from_slice(&(s.len() as u64).to_le_bytes());
            self.buf.extend_from_slice(s.as_bytes());
        }

        fn kv_string(&mut self, key: &str, val: &str) {
            self.str(key);
            self.buf.extend_from_slice(&vtype::STRING.to_le_bytes());
            self.str(val);
            self.kv_count += 1;
        }

        fn kv_u32(&mut self, key: &str, val: u32) {
            self.str(key);
            self.buf.extend_from_slice(&vtype::UINT32.to_le_bytes());
            self.buf.extend_from_slice(&val.to_le_bytes());
            self.kv_count += 1;
        }

        /// A string-array KV (like a tokenizer vocab) — exercises array skipping.
        fn kv_string_array(&mut self, key: &str, vals: &[&str]) {
            self.str(key);
            self.buf.extend_from_slice(&vtype::ARRAY.to_le_bytes());
            self.buf.extend_from_slice(&vtype::STRING.to_le_bytes());
            self.buf.extend_from_slice(&(vals.len() as u64).to_le_bytes());
            for v in vals {
                self.str(v);
            }
            self.kv_count += 1;
        }

        fn finish(self, version: u32, tensor_count: u64) -> Vec<u8> {
            let mut out = Vec::new();
            out.write_all(&GGUF_MAGIC).unwrap();
            out.write_all(&version.to_le_bytes()).unwrap();
            out.write_all(&tensor_count.to_le_bytes()).unwrap();
            out.write_all(&self.kv_count.to_le_bytes()).unwrap();
            out.extend_from_slice(&self.buf);
            out
        }
    }

    fn sample_llama() -> Vec<u8> {
        let mut w = GgufWriter::new();
        w.kv_string("general.architecture", "llama");
        w.kv_string("general.name", "tiny-test-llama");
        // A vocab array placed *between* scalars: if array-skipping is wrong,
        // every field after this one would be misread.
        w.kv_string_array("tokenizer.ggml.tokens", &["<s>", "</s>", "hello", "world"]);
        w.kv_u32("llama.block_count", 32);
        w.kv_u32("llama.context_length", 4096);
        w.kv_u32("llama.embedding_length", 4096);
        w.kv_u32("llama.attention.head_count", 32);
        w.kv_u32("llama.attention.head_count_kv", 8);
        w.kv_u32("llama.expert_count", 0);
        w.kv_u32("general.file_type", 15); // Q4_K_M
        w.finish(3, 291)
    }

    #[test]
    fn reads_scalar_fields_across_an_array() {
        let bytes = sample_llama();
        let meta = read_metadata(&mut Cursor::new(bytes)).unwrap();
        assert_eq!(meta.version, 3);
        assert_eq!(meta.tensor_count, 291);
        assert_eq!(meta.architecture(), Some("llama"));
        assert_eq!(meta.name(), Some("tiny-test-llama"));
        assert_eq!(meta.layer_count(), Some(32));
        assert_eq!(meta.context_length(), Some(4096));
        assert_eq!(meta.embedding_length(), Some(4096));
        assert_eq!(meta.head_count(), Some(32));
        assert_eq!(meta.head_count_kv(), Some(8));
        assert_eq!(meta.quantization().as_deref(), Some("Q4_K_M"));
        assert!(!meta.is_moe());

        // The array survived as a summary.
        match meta.kv.get("tokenizer.ggml.tokens") {
            Some(GgufValue::Array { elem_type, len }) => {
                assert_eq!(*elem_type, vtype::STRING);
                assert_eq!(*len, 4);
            }
            other => panic!("expected array summary, got {other:?}"),
        }
    }

    #[test]
    fn discovery_summary_is_populated() {
        let meta = read_metadata(&mut Cursor::new(sample_llama())).unwrap();
        let d = meta.discovery();
        assert_eq!(d.architecture.as_deref(), Some("llama"));
        assert_eq!(d.layers, Some(32));
        assert_eq!(d.quantization.as_deref(), Some("Q4_K_M"));
        assert!(!d.is_moe);
    }

    #[test]
    fn detects_moe() {
        let mut w = GgufWriter::new();
        w.kv_string("general.architecture", "qwen2moe");
        w.kv_u32("qwen2moe.block_count", 24);
        w.kv_u32("qwen2moe.expert_count", 60);
        let bytes = w.finish(3, 10);
        let meta = read_metadata(&mut Cursor::new(bytes)).unwrap();
        assert_eq!(meta.expert_count(), Some(60));
        assert!(meta.is_moe());
    }

    #[test]
    fn bad_magic_is_rejected() {
        let bytes = b"NOPExxxxxxxxxxxx".to_vec();
        let err = read_metadata(&mut Cursor::new(bytes)).unwrap_err();
        assert!(matches!(err, GgufError::BadMagic));
    }

    #[test]
    fn unsupported_version_is_rejected() {
        let mut bytes = Vec::new();
        bytes.extend_from_slice(&GGUF_MAGIC);
        bytes.extend_from_slice(&1u32.to_le_bytes());
        bytes.extend_from_slice(&0u64.to_le_bytes());
        bytes.extend_from_slice(&0u64.to_le_bytes());
        let err = read_metadata(&mut Cursor::new(bytes)).unwrap_err();
        assert!(matches!(err, GgufError::UnsupportedVersion(1)));
    }

    #[test]
    fn truncated_header_errors_not_panics() {
        let bytes = b"GGUF".to_vec(); // magic only, nothing after
        let err = read_metadata(&mut Cursor::new(bytes)).unwrap_err();
        assert!(matches!(err, GgufError::Io(_)));
    }

    #[test]
    fn file_type_name_maps_known_and_unknown() {
        assert_eq!(file_type_name(15), "Q4_K_M");
        assert_eq!(file_type_name(1), "F16");
        assert_eq!(file_type_name(9999), "FTYPE_9999");
    }

    #[test]
    fn reads_from_a_real_temp_file() {
        use std::io::Write as _;
        let mut f = tempfile::NamedTempFile::new().unwrap();
        f.write_all(&sample_llama()).unwrap();
        let meta = read_metadata_from_file(f.path()).unwrap();
        assert_eq!(meta.architecture(), Some("llama"));
        assert_eq!(meta.layer_count(), Some(32));
    }
}
