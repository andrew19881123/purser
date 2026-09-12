// Route: /platform/users — add to router.tsx when ready
//
// PlatformUsersPage — cross-org user directory.
//
// Shows all platform users with their org membership, role, and last active
// time. An org filter lets operators scope the view to a single organization.
//
// Design language: directory listing. Each user is anchored by an avatar
// initial chip generated from their identifier. Users inactive for >30 days
// appear at reduced opacity to surface who is active.
import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  LoadingBlock,
  PageHeader,
  type Tone,
} from '../components/ui';
import { usePlatformUsers } from '../hooks/queries';
import { relativeTime } from '../lib/format';
import type { PlatformUser } from '../api/types';

// ---------------------------------------------------------------------------
// Avatar chip — initials derived from the user identifier
// ---------------------------------------------------------------------------

/** Deterministic hue (0-360) from a string so each user gets a consistent color. */
function stringHue(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) & 0xffff;
  return h % 360;
}

function AvatarChip({ id }: { id: string }) {
  const initial = id.trim().charAt(0).toUpperCase() || '?';
  const hue = stringHue(id);
  return (
    <span
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        width: '28px',
        height: '28px',
        borderRadius: '50%',
        background: `hsl(${hue}, 55%, 45%)`,
        color: '#fff',
        fontSize: '12px',
        fontWeight: 700,
        flexShrink: 0,
      }}
      aria-hidden="true"
    >
      {initial}
    </span>
  );
}

// ---------------------------------------------------------------------------
// Role badge
// ---------------------------------------------------------------------------

const ROLE_TONE: Record<string, Tone> = {
  admin: 'warning',
  member: 'neutral',
  viewer: 'info',
  org_admin: 'warning',
};

function UserRoleBadge({ role }: { role: string }) {
  return (
    <span data-testid="user-role-badge">
      <Badge tone={ROLE_TONE[role] ?? 'neutral'}>{role}</Badge>
    </span>
  );
}

// ---------------------------------------------------------------------------
// Team chips — inline list of team memberships
// ---------------------------------------------------------------------------

function TeamChips({ teams }: { teams: string[] }) {
  if (!teams || teams.length === 0) return <span className="muted">—</span>;
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '4px' }}>
      {teams.map((t) => (
        <span
          key={t}
          style={{
            display: 'inline-block',
            padding: '1px 7px',
            borderRadius: 'var(--radius-sm)',
            fontSize: '12px',
            background: 'var(--surface-2)',
            border: '1px solid var(--border)',
            fontFamily: 'var(--font-mono)',
          }}
        >
          {t}
        </span>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// User row — with optional side-panel expansion
// ---------------------------------------------------------------------------

function UserRow({ user }: { user: PlatformUser }) {
  const [expanded, setExpanded] = useState(false);

  // Consider a user "inactive" if last active > 30 days ago or null.
  const isInactive =
    !user.lastActiveAt ||
    Date.now() - new Date(user.lastActiveAt).getTime() > 30 * 86_400_000;

  return (
    <>
      <tr
        style={{ opacity: isInactive && !expanded ? 0.65 : 1, cursor: 'pointer' }}
        onClick={() => setExpanded((e) => !e)}
        tabIndex={0}
        aria-expanded={expanded}
        onKeyDown={(e) => e.key === 'Enter' && setExpanded((x) => !x)}
      >
        <th scope="row">
          <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
            <AvatarChip id={user.id} />
            <div>
              <span style={{ fontWeight: 600, display: 'block' }} data-testid="user-display-name">
                {user.displayName !== user.id ? user.displayName : null}
              </span>
              <code
                style={{ fontFamily: 'var(--font-mono)', fontSize: '12px', color: 'var(--text-muted)' }}
                data-testid="user-email"
              >
                {user.email}
              </code>
            </div>
          </div>
        </th>
        <td>
          <span style={{ fontSize: '13px' }}>{user.orgName || user.orgId}</span>
        </td>
        <td><TeamChips teams={user.teams} /></td>
        <td><UserRoleBadge role={user.role} /></td>
        <td>
          {user.lastActiveAt ? (
            <span style={{ fontSize: '13px', color: isInactive ? 'var(--text-muted)' : undefined }}>
              {relativeTime(user.lastActiveAt)}
            </span>
          ) : (
            <span className="muted">never</span>
          )}
        </td>
      </tr>

      {expanded && (
        <tr>
          <td colSpan={5} style={{ padding: '0 8px 10px 8px', borderTop: 0 }}>
            <div
              style={{
                background: 'var(--surface-2)',
                borderRadius: 'var(--radius)',
                padding: '14px 18px',
                display: 'grid',
                gridTemplateColumns: '1fr 1fr',
                gap: '16px',
              }}
            >
              <div>
                <p style={{ fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '8px' }}>
                  User identifier
                </p>
                <code style={{ fontFamily: 'var(--font-mono)', fontSize: '12px' }}>{user.id}</code>
              </div>
              <div>
                <p style={{ fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '8px' }}>
                  Organization
                </p>
                <code style={{ fontFamily: 'var(--font-mono)', fontSize: '12px' }}>{user.orgId}</code>
              </div>
              <div>
                <p style={{ fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '8px' }}>
                  Teams
                </p>
                <TeamChips teams={user.teams} />
              </div>
              <div>
                <p style={{ fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)', marginBottom: '8px' }}>
                  Role
                </p>
                <UserRoleBadge role={user.role} />
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Invite button — shows informational dialog about OIDC/LDAP requirement
// ---------------------------------------------------------------------------

function InviteInfoModal({ onClose }: { onClose: () => void }) {
  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(8,12,22,0.55)',
        display: 'grid',
        placeItems: 'center',
        padding: '20px',
        zIndex: 200,
      }}
      onMouseDown={onClose}
    >
      <div
        style={{
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-lg)',
          padding: '24px',
          maxWidth: '420px',
          width: '100%',
        }}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <h2 style={{ fontSize: '16px', fontWeight: 700, marginBottom: '12px' }}>Invite users</h2>
        <div className="notice" role="status">
          Configure LDAP or OIDC to enable user management. Once an identity provider is connected,
          users will be provisioned automatically on first login.
        </div>
        <p style={{ marginTop: '14px', fontSize: '13.5px', color: 'var(--text-muted)' }}>
          See the platform documentation for LDAP and OIDC integration guides.
        </p>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '16px' }}>
          <Button variant="primary" onClick={onClose}>Close</Button>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function PlatformUsersPage() {
  const { data, isLoading, isError, error, refetch } = usePlatformUsers();
  const [orgFilter, setOrgFilter] = useState<string>('');
  const [showInvite, setShowInvite] = useState(false);

  // Collect unique orgs for the filter dropdown.
  const orgs = data
    ? Array.from(new Set(data.map((u) => u.orgId).filter(Boolean)))
    : [];

  const filtered = data
    ? orgFilter
      ? data.filter((u) => u.orgId === orgFilter)
      : data
    : [];

  return (
    <div className="page">
      <PageHeader
        title="Platform Users"
        subtitle="All users with access to this platform, across every organization."
        actions={
          <div style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
            {orgs.length > 0 && (
              <select
                className="input select--compact"
                value={orgFilter}
                onChange={(e) => setOrgFilter(e.target.value)}
                aria-label="Filter by organization"
                data-testid="org-filter"
              >
                <option value="">All organizations</option>
                {orgs.map((org) => (
                  <option key={org} value={org}>{org}</option>
                ))}
              </select>
            )}
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setShowInvite(true)}
              data-testid="invite-user-btn"
            >
              Invite user
            </Button>
          </div>
        }
      />

      <Card title={orgFilter ? `Users in ${orgFilter}` : 'All users'}>
        {isLoading && <LoadingBlock />}
        {isError && (
          <ErrorState
            message={error instanceof Error ? error.message : 'Failed to load users'}
            onRetry={() => refetch()}
          />
        )}
        {!isLoading && !isError && filtered.length === 0 && (
          <EmptyState
            message={
              orgFilter
                ? `No users in "${orgFilter}". Try clearing the organization filter.`
                : 'No users found. Users appear here after their first login via OIDC or LDAP.'
            }
          />
        )}
        {filtered.length > 0 && (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">User</th>
                  <th scope="col">Organization</th>
                  <th scope="col">Teams</th>
                  <th scope="col">Role</th>
                  <th scope="col">Last active</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((u) => (
                  <UserRow key={`${u.id}-${u.orgId}`} user={u} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {showInvite && <InviteInfoModal onClose={() => setShowInvite(false)} />}
    </div>
  );
}
