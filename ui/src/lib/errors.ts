// ---------------------------------------------------------------------------
// Map a query/mutation error onto an ACTIONABLE, localized message.
//
// React Query surfaces whatever the client threw. The real client throws
// `ApiError` (with an HTTP `status`); the mock throws plain errors. We turn the
// well-known HTTP statuses into the actionable guidance the UX promises
// (401/403/404/429/503/504 + transport failures), falling back to a
// context-specific message key supplied by the caller.
// ---------------------------------------------------------------------------
import { ApiError } from '../api/http';
import type { StringKey } from '../i18n/en';
import type { TFunc } from '../i18n';

export function errorMessage(error: unknown, t: TFunc, fallbackKey: StringKey): string {
  if (error instanceof ApiError) {
    switch (error.status) {
      case 0:
        return t('error.network');
      case 401:
        return t('error.401');
      case 403:
        return t('error.403');
      case 404:
        // The context message ("... is not in the catalog") is the right 404 text.
        return t(fallbackKey);
      case 429:
        return t('error.429');
      case 503:
        return t('error.503');
      case 504:
        return t('error.504');
      default:
        // Prefer a server-provided message when it is specific enough.
        return error.message && !/^HTTP \d+$/.test(error.message)
          ? error.message
          : t(fallbackKey);
    }
  }
  return t(fallbackKey);
}
