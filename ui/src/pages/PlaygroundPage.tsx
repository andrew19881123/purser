import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  PageHeader,
  useFieldId,
} from '../components/ui';
import { IconChat } from '../components/icons';
import { makeChat } from '../api/client';
import { useDeployments, useGatewayModels } from '../hooks/queries';
import { useT } from '../i18n';
import type { ChatMessage } from '../api/types';

const DEFAULT_MODEL = 'qwen3-moe-235b';
const SYSTEM_PROMPT = 'You are a helpful assistant running on a private Purser cluster.';
const KEY_STORAGE = 'purser.gatewayKey';

export function PlaygroundPage() {
  const t = useT();
  const deployments = useDeployments();

  // Gateway Bearer key (persisted locally); rebuilds the chat client on change.
  const [apiKey, setApiKey] = useState<string>(() => sessionStorage.getItem(KEY_STORAGE) ?? '');
  const chat = useMemo(() => makeChat(apiKey.trim() || undefined), [apiKey]);
  const gatewayModels = useGatewayModels(chat);

  // Model options: prefer the Gateway's served list (GET /v1/models); fall back
  // to the models of the active deployments if the Gateway list is unavailable.
  const deploymentModels = useMemo(
    () =>
      (deployments.data ?? [])
        .filter((d) => d.state === 'active')
        .map((d) => d.plan.modelId),
    [deployments.data],
  );
  const activeModels = useMemo(() => {
    const served = (gatewayModels.data ?? []).map((m) => m.id);
    return served.length > 0 ? served : deploymentModels;
  }, [gatewayModels.data, deploymentModels]);

  const [model, setModel] = useState(DEFAULT_MODEL);
  useEffect(() => {
    if (activeModels.length > 0 && !activeModels.includes(model)) setModel(activeModels[0]);
  }, [activeModels, model]);

  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const logRef = useRef<HTMLDivElement>(null);
  const modelId = useFieldId('model');
  const keyFieldId = useFieldId('gwkey');

  const onApiKeyChange = (value: string) => {
    setApiKey(value);
    if (value.trim()) sessionStorage.setItem(KEY_STORAGE, value.trim());
    else sessionStorage.removeItem(KEY_STORAGE);
  };

  useEffect(() => {
    logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
  }, [messages, streaming]);

  const send = () => {
    const text = input.trim();
    if (!text || streaming) return;
    const history: ChatMessage[] = [
      { role: 'system', content: SYSTEM_PROMPT },
      ...messages,
      { role: 'user', content: text },
    ];
    // Optimistically add the user message + an empty assistant slot to stream into.
    setMessages((m) => [...m, { role: 'user', content: text }, { role: 'assistant', content: '' }]);
    setInput('');
    setStreaming(true);

    const controller = new AbortController();
    abortRef.current = controller;

    chat.streamChat(
      { model, messages: history, stream: true },
      {
        signal: controller.signal,
        onToken: (tok) =>
          setMessages((m) => {
            const next = [...m];
            const last = next[next.length - 1];
            if (last?.role === 'assistant') next[next.length - 1] = { ...last, content: last.content + tok };
            return next;
          }),
        onDone: () => {
          setStreaming(false);
          abortRef.current = null;
        },
        onError: () => {
          setStreaming(false);
          abortRef.current = null;
          setMessages((m) => {
            const next = [...m];
            const last = next[next.length - 1];
            if (last?.role === 'assistant' && last.content === '') {
              next[next.length - 1] = {
                ...last,
                content: `⚠️ ${t('playground.error')}`,
              };
            }
            return next;
          });
        },
      },
    );
  };

  const stop = () => {
    abortRef.current?.abort();
    setStreaming(false);
  };

  const clear = () => {
    if (streaming) stop();
    setMessages([]);
  };

  const endpoint = `${chat.baseUrl}/chat/completions`;

  return (
    <div className="page page--chat">
      <PageHeader
        title={t('playground.title')}
        subtitle={t('playground.subtitle')}
        actions={
          <Button variant="ghost" size="sm" onClick={clear} disabled={messages.length === 0}>
            {t('playground.clear')}
          </Button>
        }
      />

      {deployments.data && activeModels.length === 0 && (
        <div className="notice" role="note">
          {t('playground.noDeployment')}{' '}
          <Link to="/catalog" className="link">
            {t('nav.catalog')}
          </Link>
        </div>
      )}

      <Card className="chat">
        <div className="chat__toolbar">
          <Field label={t('playground.model')} htmlFor={modelId}>
            <select
              id={modelId}
              className="select"
              value={model}
              onChange={(e) => setModel(e.target.value)}
            >
              {(activeModels.length > 0 ? activeModels : [DEFAULT_MODEL]).map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t('playground.apikey')} htmlFor={keyFieldId} hint={t('playground.apikeyHint')}>
            <input
              id={keyFieldId}
              className="input"
              type="password"
              autoComplete="off"
              placeholder="sk-purser-…"
              value={apiKey}
              onChange={(e) => onApiKeyChange(e.target.value)}
            />
          </Field>
          <Badge tone="neutral">{t('playground.endpointNote', { endpoint })}</Badge>
        </div>

        <div className="chat__log" role="log" aria-live="polite" aria-relevant="additions" ref={logRef}>
          {messages.length === 0 && (
            <EmptyState icon={<IconChat />} message={t('playground.empty')} />
          )}
          {messages.map((m, i) => (
            <div key={i} className={`bubble bubble--${m.role}`}>
              <span className="bubble__role">
                {m.role === 'user' ? t('playground.you') : t('playground.assistant')}
              </span>
              <div className="bubble__content">
                {m.content}
                {streaming && i === messages.length - 1 && m.role === 'assistant' && (
                  <span className="caret" aria-hidden="true" />
                )}
              </div>
            </div>
          ))}
        </div>

        <form
          className="chat__composer"
          onSubmit={(e) => {
            e.preventDefault();
            send();
          }}
        >
          <label className="visually-hidden" htmlFor="chat-input">
            {t('playground.placeholder')}
          </label>
          <textarea
            id="chat-input"
            className="chat__input"
            rows={2}
            placeholder={t('playground.placeholder')}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                send();
              }
            }}
          />
          {streaming ? (
            <Button variant="danger" onClick={stop}>
              {t('playground.stop')}
            </Button>
          ) : (
            <Button variant="primary" type="submit" disabled={!input.trim()}>
              {t('playground.send')}
            </Button>
          )}
        </form>
      </Card>
    </div>
  );
}
