// App shell: skip-link, sidebar navigation, top bar (language + theme), and the
// routed content region. Semantic landmarks (<nav>, <main>) and a keyboard skip
// link make the whole app navigable without a mouse.
import { NavLink, Outlet } from 'react-router-dom';
import { useI18n, useT, LOCALES, type Locale } from '../i18n';
import { useTheme } from '../lib/theme';
import {
  IconBox,
  IconRocket,
  IconServer,
  IconGrid,
  IconKey,
  IconLayers,
  IconChat,
  IconSettings,
  IconShield,
  IconMoon,
  IconSun,
} from './icons';
import type { StringKey } from '../i18n/en';
import type { ReactNode } from 'react';

interface NavItem {
  to: string;
  labelKey: StringKey;
  icon: ReactNode;
  end?: boolean;
}

const OPERATE: NavItem[] = [
  { to: '/', labelKey: 'nav.gettingStarted', icon: <IconRocket />, end: true },
  { to: '/fleet', labelKey: 'nav.fleet', icon: <IconServer /> },
  { to: '/catalog', labelKey: 'nav.catalog', icon: <IconGrid /> },
  { to: '/model-studio', labelKey: 'nav.modelStudio', icon: <IconBox /> },
  { to: '/deployments', labelKey: 'nav.deployments', icon: <IconLayers /> },
  { to: '/audit', labelKey: 'nav.audit', icon: <IconShield /> },
  { to: '/approvals', labelKey: 'nav.approvals', icon: <IconShield /> },
  { to: '/join-token', labelKey: 'nav.joinTokens', icon: <IconKey /> },
];
const USE: NavItem[] = [
  { to: '/playground', labelKey: 'nav.playground', icon: <IconChat /> },
  { to: '/settings', labelKey: 'nav.settings', icon: <IconSettings /> },
];

function NavGroup({ titleKey, items }: { titleKey: StringKey; items: NavItem[] }) {
  const t = useT();
  return (
    <div className="nav__group">
      <p className="nav__group-title">{t(titleKey)}</p>
      <ul className="nav__list">
        {items.map((item) => (
          <li key={item.to}>
            <NavLink
              to={item.to}
              end={item.end}
              className={({ isActive }) => `nav__link${isActive ? ' nav__link--active' : ''}`}
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
    <button className="icon-btn" onClick={toggle} aria-label={t('theme.toggle')} title={t('theme.toggle')}>
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
          <NavGroup titleKey="nav.section.operate" items={OPERATE} />
          <NavGroup titleKey="nav.section.use" items={USE} />
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
