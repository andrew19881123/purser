// ---------------------------------------------------------------------------
// OpenAI-compatible chat client for the Playground.
//
// Purser's Gateway exposes the inference plane under `/v1/...` in the OpenAI
// dialect (see docs 03_API_Gateway). This client is deliberately transport-
// pluggable: today it runs on a mock SSE transport; in Phase 2 you construct it
// with `baseUrl` pointing at the real Gateway and the `sseChatTransport` below.
// The React components never learn which transport is in use.
// ---------------------------------------------------------------------------
import type { ChatCompletionRequest, OpenAIModel } from './types';

export interface ChatStreamHandlers {
  onToken: (token: string) => void;
  onDone: (finishReason: 'stop' | 'length') => void;
  onError: (error: Error) => void;
  /** abort the in-flight stream (maps to fetch AbortSignal in Phase 2) */
  signal?: AbortSignal;
}

export interface ChatTransport {
  stream(req: ChatCompletionRequest, handlers: ChatStreamHandlers): void;
}

export interface ChatClientOptions {
  /** Gateway base URL, e.g. "/v1" (served by control plane) or "https://gw:8443/v1". */
  baseUrl: string;
  /** Bearer API key (Authorization: Bearer <key>), as OpenAI expects. */
  apiKey?: string;
  transport: ChatTransport;
  /** Served-model lister. Defaults to GET {baseUrl}/models on the real Gateway. */
  listModels?: () => Promise<OpenAIModel[]>;
}

export interface ChatClient {
  readonly baseUrl: string;
  streamChat(req: ChatCompletionRequest, handlers: ChatStreamHandlers): void;
  /** GET /v1/models — the models the Gateway is currently serving. */
  listModels(): Promise<OpenAIModel[]>;
}

export function createChatClient(opts: ChatClientOptions): ChatClient {
  return {
    baseUrl: opts.baseUrl,
    streamChat(req, handlers) {
      opts.transport.stream(req, handlers);
    },
    listModels() {
      return (opts.listModels ?? (() => fetchOpenAIModels(opts.baseUrl, opts.apiKey)))();
    },
  };
}

// ---------------------------------------------------------------------------
// PHASE 2 — real Gateway SSE transport. Kept here (unused) as the drop-in that
// replaces the mock. It reads the standard OpenAI `data: {json}\n\n` / `[DONE]`
// event stream. Wire it up by passing it to `createChatClient`.
// ---------------------------------------------------------------------------
export function makeSseChatTransport(opts: { baseUrl: string; apiKey?: string }): ChatTransport {
  return {
    stream(req, handlers) {
      const controller = new AbortController();
      opts /* referenced to keep signature meaningful */;
      handlers.signal?.addEventListener('abort', () => controller.abort(), { once: true });

      fetch(`${opts.baseUrl}/chat/completions`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...(opts.apiKey ? { Authorization: `Bearer ${opts.apiKey}` } : {}),
        },
        body: JSON.stringify({ ...req, stream: true }),
        signal: controller.signal,
      })
        .then(async (res) => {
          if (!res.ok || !res.body) {
            throw new Error(`Gateway responded ${res.status}`);
          }
          const reader = res.body.getReader();
          const decoder = new TextDecoder();
          let buffer = '';
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            buffer += decoder.decode(value, { stream: true });
            const events = buffer.split('\n\n');
            buffer = events.pop() ?? '';
            for (const evt of events) {
              const line = evt.replace(/^data:\s*/, '').trim();
              if (!line) continue;
              if (line === '[DONE]') {
                handlers.onDone('stop');
                return;
              }
              try {
                const json = JSON.parse(line);
                const delta = json.choices?.[0]?.delta?.content ?? '';
                if (delta) handlers.onToken(delta);
              } catch {
                /* ignore keep-alive/comment lines */
              }
            }
          }
          handlers.onDone('stop');
        })
        .catch((err: unknown) => {
          if (controller.signal.aborted) return;
          handlers.onError(err instanceof Error ? err : new Error(String(err)));
        });
    },
  };
}

/** GET /v1/models. Returns the OpenAI-shaped served-model list. */
export async function fetchOpenAIModels(baseUrl: string, apiKey?: string): Promise<OpenAIModel[]> {
  const res = await fetch(`${baseUrl}/models`, {
    headers: apiKey ? { Authorization: `Bearer ${apiKey}` } : {},
  });
  if (!res.ok) throw new Error(`Gateway responded ${res.status}`);
  const json = await res.json();
  const data: unknown[] = Array.isArray(json?.data) ? json.data : [];
  // OpenAI emits snake_case `owned_by`; normalize to our camelCase shape.
  return data.map((raw) => {
    const m = (raw ?? {}) as Record<string, unknown>;
    return {
      id: typeof m.id === 'string' ? m.id : '',
      object: 'model' as const,
      ownedBy:
        typeof m.ownedBy === 'string'
          ? m.ownedBy
          : typeof m.owned_by === 'string'
            ? m.owned_by
            : 'purser',
    };
  });
}
