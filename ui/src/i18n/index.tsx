// ---------------------------------------------------------------------------
// Minimal, dependency-free i18n. A `t()` wrapper over per-locale string maps
// with {placeholder} interpolation. Structured so a heavier library (i18next,
// FormatJS) can replace it later without touching call sites. Locale persists
// to localStorage and sets <html lang>.
// ---------------------------------------------------------------------------
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { en, type StringKey } from './en';
import { it } from './it';

export type Locale = 'en' | 'it';

const CATALOGS: Record<Locale, Record<StringKey, string>> = { en, it };

export const LOCALES: { code: Locale; label: string }[] = [
  { code: 'en', label: 'English' },
  { code: 'it', label: 'Italiano' },
];

export type TParams = Record<string, string | number>;
export type TFunc = (key: StringKey, params?: TParams) => string;

interface I18nContextValue {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: TFunc;
}

const I18nContext = createContext<I18nContextValue | null>(null);
const STORAGE_KEY = 'purser.locale';

function interpolate(template: string, params?: TParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (_, k: string) =>
    k in params ? String(params[k]) : `{${k}}`,
  );
}

function detectInitialLocale(): Locale {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === 'en' || stored === 'it') return stored;
  const nav = navigator.language?.slice(0, 2).toLowerCase();
  return nav === 'it' ? 'it' : 'en';
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => detectInitialLocale());

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = useCallback((l: Locale) => {
    setLocaleState(l);
    localStorage.setItem(STORAGE_KEY, l);
  }, []);

  const t = useCallback<TFunc>(
    (key, params) => interpolate(CATALOGS[locale][key] ?? CATALOGS.en[key] ?? key, params),
    [locale],
  );

  const value = useMemo(() => ({ locale, setLocale, t }), [locale, setLocale, t]);
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error('useI18n must be used within I18nProvider');
  return ctx;
}

/** Convenience hook returning just the translate function. */
export function useT(): TFunc {
  return useI18n().t;
}
