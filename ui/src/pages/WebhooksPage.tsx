// Route: /platform/webhooks — add to router.tsx
//
// WebhooksPage — webhook notification management.
//
// The /api/v1/webhooks endpoint does not exist yet (404 on the demo server).
// This page is an informative placeholder that shows:
//   - What webhooks will do when available
//   - The planned event types (styled as HTTP method badges)
//   - The planned configuration format (code block)
//   - A "v0.7" roadmap chip and a subscription nudge
//
// Visual identity: HTTP-centric. Event chips look like method/path badges
// familiar to developers. The "v0.7" chip is the signature element — a muted
// violet pill that conveys "not yet, but planned" without feeling like an error.
import { useT } from '../i18n';
import { PageHeader } from '../components/ui';

// ---------------------------------------------------------------------------
// Event type data
// ---------------------------------------------------------------------------

const EVENT_TYPES = [
  { name: 'node.down',          desc: 'A cluster node becomes unreachable' },
  { name: 'node.recovered',     desc: 'A previously unreachable node comes back' },
  { name: 'deployment.failed',  desc: 'A model deployment enters the failed state' },
  { name: 'deployment.active',  desc: 'A deployment becomes fully active' },
  { name: 'cert.expiring',      desc: 'An mTLS certificate expires within 7 days' },
  { name: 'cert.renewed',       desc: 'An mTLS certificate was automatically renewed' },
  { name: 'policy.denied',      desc: 'An OPA policy blocked a request' },
  { name: 'slo.breached',       desc: 'A model SLO compliance drops below threshold' },
  { name: 'slo.recovered',      desc: 'A breached model SLO recovers' },
  { name: 'approval.requested', desc: 'A deployment requires human approval (AI Act)' },
] as const;

const PLANNED_CONFIG_YAML = `# purser.yaml — webhook configuration (v0.7 preview)
webhooks:
  - url: https://hooks.example.com/purser
    events:
      - node.down
      - deployment.failed
      - slo.breached
    secret: "your-hmac-secret"   # used to sign the X-Purser-Signature header
    enabled: true

  - url: https://events.pagerduty.com/v2/enqueue
    events:
      - node.down
      - cert.expiring
    secret: "pd-secret"
    enabled: true`.trim();

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

/** Event chip — styled like an HTTP method badge */
function EventChip({ name }: { name: string }) {
  const [prefix, suffix] = name.split('.') as [string, string];

  return (
    <div
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '0',
        borderRadius: '4px',
        overflow: 'hidden',
        fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)',
        fontSize: '0.8em',
        fontWeight: 600,
        lineHeight: 1,
        border: '1px solid color-mix(in srgb, var(--border) 80%, transparent)',
      }}
    >
      <span
        style={{
          padding: '4px 8px',
          background: 'color-mix(in srgb, var(--accent) 15%, transparent)',
          color: 'var(--accent)',
          borderRight: '1px solid color-mix(in srgb, var(--border) 80%, transparent)',
        }}
      >
        {prefix}
      </span>
      <span
        style={{
          padding: '4px 8px',
          background: 'var(--surface-2, var(--bg))',
          color: 'var(--text)',
        }}
      >
        .{suffix}
      </span>
    </div>
  );
}

/** Roadmap version chip */
function RoadmapChip({ label }: { label: string }) {
  return (
    <span
      style={{
        display: 'inline-block',
        padding: '2px 10px',
        borderRadius: '999px',
        fontSize: '0.75em',
        fontWeight: 700,
        letterSpacing: '0.04em',
        textTransform: 'uppercase',
        background: 'color-mix(in srgb, #8b5cf6 15%, transparent)',
        color: '#8b5cf6',
        border: '1px solid color-mix(in srgb, #8b5cf6 30%, transparent)',
      }}
    >
      {label}
    </span>
  );
}

/** Section heading for the placeholder */
function SectionHeading({ children }: { children: React.ReactNode }) {
  return (
    <h3
      style={{
        margin: '0 0 0.75rem 0',
        fontSize: '0.9em',
        fontWeight: 600,
        textTransform: 'uppercase',
        letterSpacing: '0.06em',
        color: 'var(--text-muted, var(--muted))',
      }}
    >
      {children}
    </h3>
  );
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

export function WebhooksPage() {
  const t = useT();

  return (
    <div className="page">
      <PageHeader
        title={t('webhooks.title')}
        subtitle={t('webhooks.subtitle')}
      />

      {/* Placeholder card */}
      <div
        style={{
          background: 'var(--surface, var(--bg))',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius)',
          overflow: 'hidden',
        }}
      >
        {/* Hero section */}
        <div
          style={{
            padding: '2rem 2rem 1.5rem',
            borderBottom: '1px solid var(--border)',
            display: 'flex',
            flexDirection: 'column',
            gap: '0.75rem',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
            <RoadmapChip label={t('webhooks.placeholder.roadmap')} />
          </div>
          <h2
            style={{
              margin: 0,
              fontSize: '1.25rem',
              fontWeight: 700,
              color: 'var(--text)',
            }}
          >
            {t('webhooks.placeholder.heading')}
          </h2>
          <p
            style={{
              margin: 0,
              fontSize: '0.92em',
              lineHeight: 1.6,
              color: 'var(--text-muted, var(--muted))',
              maxWidth: '56rem',
            }}
          >
            {t('webhooks.placeholder.desc')}
          </p>
        </div>

        {/* Two-column content area */}
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1.4fr)',
            gap: '0',
          }}
        >
          {/* Left: planned event types */}
          <div
            style={{
              padding: '1.5rem 2rem',
              borderRight: '1px solid var(--border)',
            }}
          >
            <SectionHeading>{t('webhooks.placeholder.events.title')}</SectionHeading>
            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
              {EVENT_TYPES.map((ev) => (
                <div
                  key={ev.name}
                  style={{ display: 'flex', alignItems: 'flex-start', gap: '0.75rem' }}
                >
                  <EventChip name={ev.name} />
                  <span
                    style={{
                      fontSize: '0.85em',
                      color: 'var(--text-muted, var(--muted))',
                      lineHeight: 1.5,
                      paddingTop: '2px',
                    }}
                  >
                    {ev.desc}
                  </span>
                </div>
              ))}
            </div>
          </div>

          {/* Right: planned config format */}
          <div style={{ padding: '1.5rem 2rem' }}>
            <SectionHeading>{t('webhooks.placeholder.config.title')}</SectionHeading>
            <div
              style={{
                background: '#0d1117',
                border: '1px solid #30363d',
                borderRadius: 'var(--radius)',
                overflow: 'auto',
                maxHeight: '380px',
              }}
            >
              <pre
                style={{
                  margin: 0,
                  padding: '1rem 1.2rem',
                  fontFamily: 'var(--font-mono, "Consolas", "Courier New", monospace)',
                  fontSize: '0.78em',
                  lineHeight: 1.65,
                  color: '#e6edf3',
                  overflowX: 'auto',
                  tabSize: 2,
                }}
              >
                {PLANNED_CONFIG_YAML}
              </pre>
            </div>
          </div>
        </div>

        {/* Footer callout */}
        <div
          style={{
            padding: '1rem 2rem',
            borderTop: '1px solid var(--border)',
            background: 'var(--surface-2, color-mix(in srgb, var(--border) 20%, var(--bg)))',
            display: 'flex',
            alignItems: 'center',
            gap: '0.5rem',
            flexWrap: 'wrap',
          }}
        >
          <span style={{ fontSize: '0.875em', color: 'var(--text-muted, var(--muted))' }}>
            {t('webhooks.placeholder.subscribe')}
          </span>
          <a
            href="https://github.com/andrew19881123/purser/releases"
            target="_blank"
            rel="noreferrer"
            style={{
              fontSize: '0.875em',
              color: 'var(--accent)',
              fontWeight: 600,
              textDecoration: 'none',
              whiteSpace: 'nowrap',
            }}
          >
            {t('webhooks.placeholder.subscribe.link')}
          </a>
        </div>
      </div>
    </div>
  );
}
