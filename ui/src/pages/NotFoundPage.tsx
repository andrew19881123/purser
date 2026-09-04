import { Link } from 'react-router-dom';
import { EmptyState, PageHeader } from '../components/ui';
import { useT } from '../i18n';

export function NotFoundPage() {
  const t = useT();
  return (
    <div className="page">
      <PageHeader title="404" />
      <EmptyState
        message="This page does not exist."
        action={
          <Link to="/" className="btn btn--primary btn--md">
            {t('nav.onboarding')}
          </Link>
        }
      />
    </div>
  );
}
