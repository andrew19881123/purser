// ComingSoonPage — renders the placeholder content correctly.
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { ComingSoonPage } from '../pages/ComingSoonPage';

function renderPage() {
  return render(
    <MemoryRouter>
      <I18nProvider>
        <ComingSoonPage />
      </I18nProvider>
    </MemoryRouter>,
  );
}

describe('ComingSoonPage', () => {
  it('renders the coming soon title', () => {
    renderPage();
    expect(screen.getByRole('heading', { name: /coming soon/i })).toBeInTheDocument();
  });

  it('renders the body text', () => {
    renderPage();
    expect(
      screen.getByText('This feature is under active development.'),
    ).toBeInTheDocument();
  });
});
