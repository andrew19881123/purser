// TeamPage — role picker regression test (v0.4 RBAC).
//
// (e) The "invite member" role field must be a <select> populated from the
// org's roles (built-in + custom), NOT a free-text input. Assigning a member a
// role is a pick from the roles API, not a typed string.
import { render, screen, fireEvent, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { I18nProvider } from '../../i18n';
import { TeamPage } from '../TeamPage';
import type { CustomRole, Team } from '../../api/types';

// ---------------------------------------------------------------------------
// Mocks — mock the whole hooks module so TeamPage renders without a backend.
// ---------------------------------------------------------------------------

vi.mock('../../hooks/queries', () => ({
  useTeam: vi.fn(),
  useTeamMembers: vi.fn(),
  useAddTeamMember: vi.fn(),
  useRemoveTeamMember: vi.fn(),
  useNodePools: vi.fn(),
  useMyTeamPermissions: vi.fn(),
  useRoles: vi.fn(),
}));

import {
  useTeam,
  useTeamMembers,
  useAddTeamMember,
  useRemoveTeamMember,
  useNodePools,
  useMyTeamPermissions,
  useRoles,
} from '../../hooks/queries';

const TEAM: Team = {
  id: 'team-1',
  org_id: 'org-1',
  name: 'Team One',
  slug: 'team-one',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const ROLES: CustomRole[] = [
  { id: 'developer', orgId: '', name: 'Developer', permissions: ['team:models:deploy'], isSystem: true, createdAt: '', updatedAt: '' },
  { id: 'viewer', orgId: '', name: 'Viewer', permissions: ['team:members:view'], isSystem: true, createdAt: '', updatedAt: '' },
  { id: 'role-custom', orgId: 'org-1', name: 'ML Engineer', permissions: ['inference:call'], isSystem: false, createdAt: '', updatedAt: '' },
];

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function qr(overrides: Record<string, unknown> = {}): any {
  return { data: undefined, isLoading: false, isError: false, error: null, refetch: vi.fn(), ...overrides };
}
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function mut(overrides: Record<string, unknown> = {}): any {
  return { mutate: vi.fn(), mutateAsync: vi.fn(() => Promise.resolve()), isPending: false, isError: false, error: null, ...overrides };
}

function mockAll() {
  vi.mocked(useTeam).mockReturnValue(qr({ data: TEAM }));
  vi.mocked(useTeamMembers).mockReturnValue(qr({ data: { members: [] } }));
  vi.mocked(useAddTeamMember).mockReturnValue(mut());
  vi.mocked(useRemoveTeamMember).mockReturnValue(mut());
  vi.mocked(useNodePools).mockReturnValue(qr({ data: { pools: [] } }));
  vi.mocked(useMyTeamPermissions).mockReturnValue(qr({ data: { permissions: [], is_org_admin: false } }));
  vi.mocked(useRoles).mockReturnValue(qr({ data: { roles: ROLES } }));
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/platform/orgs/org-1/teams/team-1']}>
        <I18nProvider>
          <Routes>
            <Route path="/platform/orgs/:orgId/teams/:teamId" element={<TeamPage />} />
          </Routes>
        </I18nProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mockAll();
});

describe('TeamPage — member role picker', () => {
  it('renders the role field as a <select> populated from the org roles', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /invite member/i }));

    const dialog = screen.getByRole('dialog');
    const roleField = within(dialog).getByLabelText('Role');
    expect(roleField.tagName).toBe('SELECT');

    // Options come from the roles API (built-in + custom), not a typed string.
    expect(within(dialog).getByRole('option', { name: 'Developer' })).toBeInTheDocument();
    expect(within(dialog).getByRole('option', { name: 'Viewer' })).toBeInTheDocument();
    expect(within(dialog).getByRole('option', { name: 'ML Engineer' })).toBeInTheDocument();
  });

  it('no longer renders a free-text role input', () => {
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /invite member/i }));
    const dialog = screen.getByRole('dialog');
    // A textbox named "Role"/"Role ID" must not exist anymore.
    expect(within(dialog).queryByRole('textbox', { name: /role/i })).not.toBeInTheDocument();
  });

  it('submits the selected role id to addTeamMember', () => {
    const addMutateAsync = vi.fn(() => Promise.resolve());
    vi.mocked(useAddTeamMember).mockReturnValue(mut({ mutateAsync: addMutateAsync }));
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: /invite member/i }));

    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('User ID'), { target: { value: 'alice@example.com' } });
    fireEvent.change(within(dialog).getByLabelText('Role'), { target: { value: 'viewer' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /invite member/i }));

    expect(addMutateAsync).toHaveBeenCalledWith(
      expect.objectContaining({ user_id: 'alice@example.com', role_id: 'viewer' }),
    );
  });
});
