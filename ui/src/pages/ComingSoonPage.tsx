// Placeholder for pages that are under active development.
// The router points not-yet-built routes here so nav links work
// before the real page lands.
import { useT } from '../i18n';

export function ComingSoonPage() {
  const t = useT();
  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1 className="page-header__title">{t('coming_soon.title')}</h1>
          <p className="page-header__subtitle">{t('coming_soon.body')}</p>
        </div>
      </div>
    </div>
  );
}
