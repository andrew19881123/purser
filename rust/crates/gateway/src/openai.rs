//! OpenAI-compatible request/response wire types.
//!
//! These mirror the subset of the OpenAI API the gateway speaks. The goal is
//! **drop-in compatibility**: any existing OpenAI client (SDKs, IDE plugins,
//! Open WebUI, LiteLLM, …) should work against Purser by changing only the
//! base URL. Requests deserialize leniently (unknown fields are ignored);
//! responses serialize into the exact shapes clients expect.

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};

/// Seconds since the Unix epoch (OpenAI `created` field).
pub fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// Generate an OpenAI-style opaque id such as `chatcmpl-1a2b...`.
pub fn gen_id(prefix: &str) -> String {
    static SEQ: AtomicU64 = AtomicU64::new(0);
    let seq = SEQ.fetch_add(1, Ordering::Relaxed);
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.subsec_nanos())
        .unwrap_or(0);
    format!("{prefix}-{nanos:08x}{:08x}", seq as u32)
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

/// A single chat message. `content` is kept as a plain string in the skeleton
/// (the array/multimodal form is a phase-2 concern).
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ChatMessage {
    pub role: String,
    #[serde(default)]
    pub content: String,
}

/// `POST /v1/chat/completions` request body.
#[derive(Debug, Clone, Deserialize)]
pub struct ChatCompletionRequest {
    pub model: String,
    #[serde(default)]
    pub messages: Vec<ChatMessage>,
    #[serde(default)]
    pub stream: bool,
}

/// `POST /v1/completions` (legacy) request body.
#[derive(Debug, Clone, Deserialize)]
pub struct CompletionRequest {
    pub model: String,
    #[serde(default)]
    pub prompt: String,
    #[serde(default)]
    pub stream: bool,
}

// ---------------------------------------------------------------------------
// Shared
// ---------------------------------------------------------------------------

/// Token accounting. Values are approximate in the skeleton (word counts).
#[derive(Debug, Clone, Serialize)]
pub struct Usage {
    pub prompt_tokens: u32,
    pub completion_tokens: u32,
    pub total_tokens: u32,
}

impl Usage {
    pub fn new(prompt_tokens: u32, completion_tokens: u32) -> Self {
        Self {
            prompt_tokens,
            completion_tokens,
            total_tokens: prompt_tokens + completion_tokens,
        }
    }
}

// ---------------------------------------------------------------------------
// Chat completions — non-streaming
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize)]
pub struct ChatChoice {
    pub index: u32,
    pub message: ChatMessage,
    pub finish_reason: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct ChatCompletionResponse {
    pub id: String,
    pub object: &'static str,
    pub created: u64,
    pub model: String,
    pub choices: Vec<ChatChoice>,
    pub usage: Usage,
}

impl ChatCompletionResponse {
    pub fn single(model: String, content: String, prompt_tokens: u32) -> Self {
        let completion_tokens = content.split_whitespace().count() as u32;
        Self {
            id: gen_id("chatcmpl"),
            object: "chat.completion",
            created: unix_now(),
            model,
            choices: vec![ChatChoice {
                index: 0,
                message: ChatMessage {
                    role: "assistant".to_string(),
                    content,
                },
                finish_reason: "stop".to_string(),
            }],
            usage: Usage::new(prompt_tokens, completion_tokens),
        }
    }
}

// ---------------------------------------------------------------------------
// Chat completions — streaming (SSE chunks)
// ---------------------------------------------------------------------------

/// Incremental delta inside a streaming chunk. Both fields are optional so the
/// opening chunk carries only `role`, body chunks only `content`, and the
/// final chunk an empty `{}` delta — exactly like OpenAI.
#[derive(Debug, Clone, Default, Serialize)]
pub struct Delta {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub role: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ChatChunkChoice {
    pub index: u32,
    pub delta: Delta,
    pub finish_reason: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct ChatCompletionChunk {
    pub id: String,
    pub object: &'static str,
    pub created: u64,
    pub model: String,
    pub choices: Vec<ChatChunkChoice>,
}

impl ChatCompletionChunk {
    fn with(id: &str, created: u64, model: &str, delta: Delta, finish: Option<&str>) -> Self {
        Self {
            id: id.to_string(),
            object: "chat.completion.chunk",
            created,
            model: model.to_string(),
            choices: vec![ChatChunkChoice {
                index: 0,
                delta,
                finish_reason: finish.map(str::to_string),
            }],
        }
    }

    /// Opening chunk: announces the assistant role.
    pub fn role(id: &str, created: u64, model: &str) -> Self {
        Self::with(
            id,
            created,
            model,
            Delta {
                role: Some("assistant".to_string()),
                content: None,
            },
            None,
        )
    }

    /// Body chunk carrying one piece of content.
    pub fn content(id: &str, created: u64, model: &str, content: String) -> Self {
        Self::with(
            id,
            created,
            model,
            Delta {
                role: None,
                content: Some(content),
            },
            None,
        )
    }

    /// Final chunk: empty delta + `finish_reason`.
    pub fn stop(id: &str, created: u64, model: &str) -> Self {
        Self::with(id, created, model, Delta::default(), Some("stop"))
    }
}

// ---------------------------------------------------------------------------
// Legacy completions
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize)]
pub struct CompletionChoice {
    pub index: u32,
    pub text: String,
    pub finish_reason: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct CompletionResponse {
    pub id: String,
    pub object: &'static str,
    pub created: u64,
    pub model: String,
    pub choices: Vec<CompletionChoice>,
    pub usage: Usage,
}

impl CompletionResponse {
    pub fn single(model: String, text: String, prompt_tokens: u32) -> Self {
        let completion_tokens = text.split_whitespace().count() as u32;
        Self {
            id: gen_id("cmpl"),
            object: "text_completion",
            created: unix_now(),
            model,
            choices: vec![CompletionChoice {
                index: 0,
                text,
                finish_reason: Some("stop".to_string()),
            }],
            usage: Usage::new(prompt_tokens, completion_tokens),
        }
    }
}

/// One streaming chunk for the legacy completions endpoint.
#[derive(Debug, Clone, Serialize)]
pub struct CompletionChunk {
    pub id: String,
    pub object: &'static str,
    pub created: u64,
    pub model: String,
    pub choices: Vec<CompletionChoice>,
}

impl CompletionChunk {
    pub fn piece(id: &str, created: u64, model: &str, text: String, finish: Option<&str>) -> Self {
        Self {
            id: id.to_string(),
            object: "text_completion",
            created,
            model: model.to_string(),
            choices: vec![CompletionChoice {
                index: 0,
                text,
                finish_reason: finish.map(str::to_string),
            }],
        }
    }
}

// ---------------------------------------------------------------------------
// Models listing
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize)]
pub struct ModelObject {
    pub id: String,
    pub object: &'static str,
    pub created: u64,
    pub owned_by: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct ModelList {
    pub object: &'static str,
    pub data: Vec<ModelObject>,
}
