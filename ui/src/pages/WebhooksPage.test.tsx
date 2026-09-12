// WebhooksPage tests — placeholder rendering, event chips, config code block,
// roadmap chip, and subscription link.
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { I18nProvider } from '../i18n';
import { WebhooksPage } from './WebhooksPage';

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

function renderPage() {
  return render(
    <MemoryRouter>
      <I18nProvider>
        <WebhooksPage />
      </I18nProvider>
    </MemoryRouter>,
  );
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('WebhooksPage — placeholder renders', () => {
  it('renders the page title', () => {
    renderPage();
    expect(screen.getByRole('heading', { name: /webhooks/i })).toBeInTheDocument();
  });

  it('renders the page subtitle', () => {
    renderPage();
    expect(screen.getByText(/event notifications/i)).toBeInTheDocument();
  });

  it('shows "Coming in v0.7" heading', () => {
    renderPage();
    expect(screen.getByText(/coming in v0\.7/i)).toBeInTheDocument();
  });

  it('shows the v0.7 roadmap chip', () => {
    renderPage();
    expect(screen.getByText('v0.7')).toBeInTheDocument();
  });

  it('shows the placeholder description text', () => {
    renderPage();
    expect(screen.getByText(/active development/i)).toBeInTheDocument();
  });
});

describe('WebhooksPage — event types section', () => {
  it('shows "Planned event types" section heading', () => {
    renderPage();
    expect(screen.getByText(/planned event types/i)).toBeInTheDocument();
  });

  it('renders node.down event', () => {
    renderPage();
    // "node" prefix appears for multiple event chips (node.down, node.recovered, etc.)
    expect(screen.getAllByText('node').length).toBeGreaterThan(0);
    // ".down" appears on the node.down chip
    expect(screen.getByText('.down')).toBeInTheDocument();
  });

  it('renders deployment.failed event', () => {
    renderPage();
    expect(screen.getByText('.failed')).toBeInTheDocument();
  });

  it('renders cert.expiring event', () => {
    renderPage();
    expect(screen.getByText('.expiring')).toBeInTheDocument();
  });

  it('renders slo.breached event', () => {
    renderPage();
    expect(screen.getByText('.breached')).toBeInTheDocument();
  });
});

describe('WebhooksPage — config format section', () => {
  it('shows "Planned configuration format" section heading', () => {
    renderPage();
    expect(screen.getByText(/planned configuration format/i)).toBeInTheDocument();
  });

  it('renders a YAML code block with webhook config', () => {
    renderPage();
    expect(screen.getByText(/purser\.yaml/i)).toBeInTheDocument();
  });

  it('config block mentions HMAC signing', () => {
    renderPage();
    expect(screen.getByText(/hmac-secret/i)).toBeInTheDocument();
  });
});

describe('WebhooksPage — subscription footer', () => {
  it('shows subscription nudge text', () => {
    renderPage();
    expect(screen.getByText(/subscribe to releases/i)).toBeInTheDocument();
  });

  it('renders a GitHub releases link', () => {
    renderPage();
    const link = screen.getByRole('link', { name: /releases on github/i });
    expect(link).toBeInTheDocument();
    expect(link.getAttribute('href')).toContain('github.com');
  });

  it('GitHub link opens in a new tab', () => {
    renderPage();
    const link = screen.getByRole('link', { name: /releases on github/i });
    expect(link.getAttribute('target')).toBe('_blank');
  });
});
