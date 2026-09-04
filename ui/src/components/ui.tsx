// Reusable, accessible UI primitives. No external UI library — all styling is
// local CSS (see ../styles/global.css). Focus rings, ARIA roles and keyboard
// behavior are baked in here so pages inherit accessibility for free.
import {
  useEffect,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type ReactNode,
} from 'react';
import { IconCheck, IconCopy, IconWarning } from './icons';
import { useT } from '../i18n';
import type { NodeState } from '../api/types';

// --- Button -----------------------------------------------------------------

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger';
interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: 'sm' | 'md';
}

export function Button({
  variant = 'secondary',
  size = 'md',
  className = '',
  type = 'button',
  ...rest
}: ButtonProps) {
  return (
    <button
      type={type}
      className={`btn btn--${variant} btn--${size} ${className}`.trim()}
      {...rest}
    />
  );
}

// --- Page header -------------------------------------------------------------

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div>
        <h1 className="page-header__title">{title}</h1>
        {subtitle && <p className="page-header__subtitle">{subtitle}</p>}
      </div>
      {actions && <div className="page-header__actions">{actions}</div>}
    </div>
  );
}

// --- Card --------------------------------------------------------------------

export function Card({
  title,
  action,
  children,
  className = '',
}: {
  title?: ReactNode;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`card ${className}`.trim()}>
      {(title || action) && (
        <header className="card__head">
          {title && <h2 className="card__title">{title}</h2>}
          {action}
        </header>
      )}
      <div className="card__body">{children}</div>
    </section>
  );
}

// --- Badge -------------------------------------------------------------------

export type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

export function Badge({
  tone = 'neutral',
  children,
}: {
  tone?: Tone;
  children: ReactNode;
}) {
  return <span className={`badge badge--${tone}`}>{children}</span>;
}

// --- StatusPill (node lifecycle state) --------------------------------------

const STATE_TONE: Record<NodeState, Tone> = {
  provisioning: 'info',
  enrolled: 'info',
  ready: 'success',
  loading: 'info',
  running: 'success',
  degraded: 'warning',
  draining: 'warning',
  unreachable: 'danger',
  decommissioned: 'neutral',
};

export function StatusPill({ state }: { state: NodeState }) {
  const t = useT();
  const tone = STATE_TONE[state];
  return (
    <span className={`pill pill--${tone}`}>
      <span className="pill__dot" aria-hidden="true" />
      {t(`state.${state}`)}
    </span>
  );
}

// --- Spinner / loading ------------------------------------------------------

export function Spinner({ label }: { label?: string }) {
  const t = useT();
  return (
    <span className="spinner" role="status" aria-live="polite">
      <span className="spinner__ring" aria-hidden="true" />
      <span className="visually-hidden">{label ?? t('common.loading')}</span>
    </span>
  );
}

export function LoadingBlock({ label }: { label?: string }) {
  const t = useT();
  return (
    <div className="loading-block">
      <Spinner />
      <span>{label ?? t('common.loading')}</span>
    </div>
  );
}

// --- ErrorState (actionable) ------------------------------------------------

export function ErrorState({
  title,
  message,
  onRetry,
  action,
}: {
  title?: string;
  message: string;
  onRetry?: () => void;
  action?: ReactNode;
}) {
  const t = useT();
  return (
    <div className="error-state" role="alert">
      <span className="error-state__icon" aria-hidden="true">
        <IconWarning />
      </span>
      <div className="error-state__text">
        <p className="error-state__title">{title ?? t('error.title')}</p>
        <p className="error-state__msg">{message}</p>
      </div>
      <div className="error-state__actions">
        {onRetry && (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            {t('action.retry')}
          </Button>
        )}
        {action}
      </div>
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  message,
  action,
}: {
  icon?: ReactNode;
  title?: string;
  message: string;
  action?: ReactNode;
}) {
  const t = useT();
  return (
    <div className="empty-state">
      {icon && <span className="empty-state__icon" aria-hidden="true">{icon}</span>}
      <p className="empty-state__title">{title ?? t('empty.title')}</p>
      <p className="empty-state__msg">{message}</p>
      {action}
    </div>
  );
}

// --- Copy button + code block -----------------------------------------------

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Fallback for non-secure contexts / older engines.
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    let ok = false;
    try {
      ok = document.execCommand('copy');
    } catch {
      ok = false;
    }
    document.body.removeChild(ta);
    return ok;
  }
}

export function CopyButton({ value, label }: { value: string; label?: string }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={async () => {
        if (await copyText(value)) {
          setCopied(true);
          window.clearTimeout(timer.current);
          timer.current = window.setTimeout(() => setCopied(false), 1600);
        }
      }}
      aria-label={label ?? t('action.copy')}
    >
      {copied ? <IconCheck /> : <IconCopy />}
      <span>{copied ? t('action.copied') : t('action.copy')}</span>
    </Button>
  );
}

export function CodeBlock({ code, ariaLabel }: { code: string; ariaLabel?: string }) {
  return (
    <div className="codeblock">
      <pre className="codeblock__pre" tabIndex={0} aria-label={ariaLabel}>
        <code>{code}</code>
      </pre>
      <div className="codeblock__actions">
        <CopyButton value={code} />
      </div>
    </div>
  );
}

// --- Progress + meter -------------------------------------------------------

export function ProgressBar({ value, label }: { value: number; label?: string }) {
  const pct = Math.round(value * 100);
  return (
    <div
      className="progress"
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={label}
    >
      <div className="progress__fill" style={{ width: `${pct}%` }} />
    </div>
  );
}

export function Meter({
  used,
  total,
  label,
  unit = 'GB',
}: {
  used: number;
  total: number;
  label: string;
  unit?: string;
}) {
  const ratio = total > 0 ? used / total : 0;
  const tone = ratio > 0.9 ? 'danger' : ratio > 0.75 ? 'warning' : 'ok';
  return (
    <div className="meter">
      <div className="meter__row">
        <span className="meter__label">{label}</span>
        <span className="meter__value">
          {used.toFixed(0)} / {total.toFixed(0)} {unit}
        </span>
      </div>
      <div
        className={`meter__track meter__track--${tone}`}
        role="meter"
        aria-valuenow={Math.round(ratio * 100)}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
      >
        <div className="meter__fill" style={{ width: `${Math.min(100, ratio * 100)}%` }} />
      </div>
    </div>
  );
}

// --- Form field / inputs ----------------------------------------------------

let fieldSeq = 0;
export function useFieldId(prefix = 'field'): string {
  const ref = useRef<string>();
  if (!ref.current) {
    fieldSeq += 1;
    ref.current = `${prefix}-${fieldSeq}`;
  }
  return ref.current;
}

export function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <label className="field__label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint && <p className="field__hint">{hint}</p>}
    </div>
  );
}

// --- Tabs (WAI-ARIA, arrow-key navigable) -----------------------------------

export interface TabItem {
  id: string;
  label: string;
}

export function Tabs({
  tabs,
  active,
  onChange,
  ariaLabel,
}: {
  tabs: TabItem[];
  active: string;
  onChange: (id: string) => void;
  ariaLabel: string;
}) {
  const onKeyDown = (e: React.KeyboardEvent, idx: number) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    e.preventDefault();
    const dir = e.key === 'ArrowRight' ? 1 : -1;
    const next = (idx + dir + tabs.length) % tabs.length;
    onChange(tabs[next].id);
  };
  return (
    <div className="tabs" role="tablist" aria-label={ariaLabel}>
      {tabs.map((tab, idx) => {
        const selected = tab.id === active;
        return (
          <button
            key={tab.id}
            role="tab"
            id={`tab-${tab.id}`}
            aria-selected={selected}
            aria-controls={`panel-${tab.id}`}
            tabIndex={selected ? 0 : -1}
            className={`tab${selected ? ' tab--active' : ''}`}
            onClick={() => onChange(tab.id)}
            onKeyDown={(e) => onKeyDown(e, idx)}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

export function TabPanel({ id, children }: { id: string; children: ReactNode }) {
  return (
    <div role="tabpanel" id={`panel-${id}`} aria-labelledby={`tab-${id}`}>
      {children}
    </div>
  );
}

// --- Modal dialog -----------------------------------------------------------

export function Modal({
  title,
  onClose,
  children,
  footer,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleId = useFieldId('dialog-title');

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    // Move focus into the dialog for keyboard/screen-reader users.
    dialogRef.current?.focus();
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div className="modal-backdrop" onMouseDown={onClose}>
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        ref={dialogRef}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <header className="modal__head">
          <h2 className="modal__title" id={titleId}>
            {title}
          </h2>
        </header>
        <div className="modal__body">{children}</div>
        {footer && <footer className="modal__foot">{footer}</footer>}
      </div>
    </div>
  );
}
