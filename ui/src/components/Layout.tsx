// App shell: skip-link, sidebar navigation, top bar (language + theme), and the
// routed content region. Semantic landmarks (<nav>, <main>) and a keyboard skip
// link make the whole app navigable without a mouse.
//
// Sidebar v0.6: 5 role-based sections (Inference, Platform, Governance,
// Observability, Administration). The active section receives a 2px left accent
// border on its label — a single purposeful signal that tells operators exactly
// where they are without per-item highlighting noise.
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useI18n, useT, LOCALES, type Locale } from '../i18n';
import { useTheme } from '../lib/theme';
import {
  IconBuildingOffice,
  IconCalculator,
  IconChart,
  IconChat,
  IconCheckCircle,
  IconDataPlanes,
  IconGrid,
  IconKey,
  IconLayers,
  IconLock,
  IconMoon,
  IconPlus,
  IconRobot,
  IconServer,
  IconSettings,
  IconShield,
  IconSun,
  IconTarget,
  IconUsers,
} from './icons';
import type { StringKey } from '../i18n/en';
import type { ReactNode } from 'react';

interface NavItem {
  to: string;
  labelKey: StringKey;
  icon: ReactNode;
  end?: boolean;
}

// ── INFERENCE ─────────────────────────────────────────────────────────────────
// Day-to-day inference operations: what models are running, what's deployed,
// how to test them. ML engineers live here.
const INFERENCE: NavItem[] = [
  { to: '/fleet',       labelKey: 'nav.fleet',       icon: <IconServer /> },
  { to: '/catalog',     labelKey: 'nav.catalog',     icon: <IconGrid /> },
  { to: '/deployments', labelKey: 'nav.deployments', icon: <IconLayers /> },
  { to: '/playground',  labelKey: 'nav.playground',  icon: <IconChat /> },
];

// ── PLATFORM ──────────────────────────────────────────────────────────────────
// Infrastructure and capacity: where compute lives and how it's planned.
// Infra engineers live here.
const PLATFORM: NavItem[] = [
  { to: '/platform/dataplanes', labelKey: 'nav.dataplanes',    icon: <IconDataPlanes /> },
  { to: '/platform/pools',      labelKey: 'nav.nodePools',     icon: <IconServer /> },
  { to: '/planner/what-if',     labelKey: 'nav.whatIfPlanner', icon: <IconCalculator /> },
];

// ── GOVERNANCE ────────────────────────────────────────────────────────────────
// Identity, access, and control: who can do what and through which key.
// Security and platform teams live here.
const GOVERNANCE: NavItem[] = [
  { to: '/platform/orgs',             labelKey: 'nav.organizations',  icon: <IconBuildingOffice /> },
  { to: '/platform/users',            labelKey: 'nav.platformUsers',  icon: <IconUsers /> },
  { to: '/api-keys',                  labelKey: 'nav.apiKeys',        icon: <IconKey /> },
  { to: '/platform/service-accounts', labelKey: 'nav.serviceAccounts', icon: <IconRobot /> },
  { to: '/platform/policies',         labelKey: 'nav.policies',       icon: <IconShield /> },
  { to: '/approvals',                 labelKey: 'nav.approvals',      icon: <IconCheckCircle /> },
];

// ── OBSERVABILITY ─────────────────────────────────────────────────────────────
// Audit, cost, and reliability: what happened, how much it cost, and whether
// SLOs are being met. Compliance and FinOps teams live here.
const OBSERVABILITY: NavItem[] = [
  { to: '/audit',      labelKey: 'nav.audit',      icon: <IconLock /> },
  { to: '/chargeback', labelKey: 'nav.chargeback', icon: <IconChart /> },
  { to: '/slo',        labelKey: 'nav.slo',        icon: <IconTarget /> },
];

// ── ADMINISTRATION ────────────────────────────────────────────────────────────
// Cluster management: enrolling new nodes and global settings.
const ADMINISTRATION: NavItem[] = [
  { to: '/join-token', labelKey: 'nav.joinTokens', icon: <IconPlus /> },
  { to: '/settings',   labelKey: 'nav.settings',   icon: <IconSettings /> },
];

// A section is "active" when the current URL lives inside one of its items.
// We check for exact match OR a sub-path (e.g. /platform/orgs/123/teams/1
// activates the /platform/orgs item) without accidentally matching siblings
// (e.g. /platform/dataplanes must not activate /platform/orgs).
function isSectionActive(items: NavItem[], pathname: string): boolean {
  return items.some((item) => {
    if (item.end) return pathname === item.to;
    return pathname === item.to || pathname.startsWith(item.to + '/');
  });
}

interface NavSectionProps {
  titleKey: StringKey;
  items: NavItem[];
}

function NavSection({ titleKey, items }: NavSectionProps) {
  const t = useT();
  const { pathname } = useLocation();
  const active = isSectionActive(items, pathname);

  return (
    <div className={`nav__section${active ? ' nav__section--active' : ''}`}>
      <p className="nav__section-label">{t(titleKey)}</p>
      <ul className="nav__list">
        {items.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end}
              className={({ isActive }) =>
                `nav__link${isActive ? ' nav__link--active' : ''}`
              }
            >
              <span className="nav__icon" aria-hidden="true">
                {item.icon}
              </span>
              <span>{t(item.labelKey)}</span>
            </NavLink>
          </li>
        ))}
      </ul>
    </div>
  );
}

function LanguagePicker() {
  const { locale, setLocale } = useI18n();
  const t = useT();
  return (
    <label className="lang-picker">
      <span className="visually-hidden">{t('lang.label')}</span>
      <select
        className="select select--compact"
        value={locale}
        onChange={(e) => setLocale(e.target.value as Locale)}
        aria-label={t('lang.label')}
      >
        {LOCALES.map((l) => (
          <option key={l.code} value={l.code}>
            {l.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function ThemeToggle() {
  const { theme, toggle } = useTheme();
  const t = useT();
  return (
    <button
      className="icon-btn"
      onClick={toggle}
      aria-label={t('theme.toggle')}
      title={t('theme.toggle')}
    >
      {theme === 'dark' ? <IconSun /> : <IconMoon />}
    </button>
  );
}

export function Layout() {
  const t = useT();
  return (
    <div className="app-shell">
      <a href="#main" className="skip-link">
        {t('skip.toContent')}
      </a>

      <aside className="sidebar">
        <div className="brand">
          <span className="brand__mark" aria-hidden="true">
            P
          </span>
          <div className="brand__text">
            <span className="brand__name">{t('app.name')}</span>
            <span className="brand__tag">{t('app.tagline')}</span>
          </div>
        </div>
        <nav className="nav" aria-label={t('app.name')}>
          <NavSection titleKey="nav.section.inference"     items={INFERENCE} />
          <NavSection titleKey="nav.section.platform"      items={PLATFORM} />
          <NavSection titleKey="nav.section.governance"    items={GOVERNANCE} />
          <NavSection titleKey="nav.section.observability" items={OBSERVABILITY} />
          <NavSection titleKey="nav.section.administration" items={ADMINISTRATION} />
        </nav>
      </aside>

      <div className="content">
        <header className="topbar">
          <div className="topbar__spacer" />
          <div className="topbar__actions">
            <LanguagePicker />
            <ThemeToggle />
          </div>
        </header>
        <main id="main" className="main" tabIndex={-1}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}
