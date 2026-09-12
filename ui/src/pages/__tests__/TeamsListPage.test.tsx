/**
 * TeamsListPage — list / create / delete, plus the OrganizationsPage
 * "View Teams" navigation that reaches it.
 *
 * The teams-list route (/platform/orgs/:orgId/teams) was missing entirely, so
 * TeamPage was only reachable by hand-typing a URL and Organizations'
 * "View Teams" button dead-ended at NotFoundPage. These tests cover the new
 * list page (built on the existing useTeams / useCreateTeam / useDeleteTeam
 * hooks) and confirm the Organizations link now resolves to a real route.
 *
 * i18n is mocked to echo keys (matching ApiKeysPage.test.tsx / SettingsPage
 * tests) so assertions are independent of translation copy.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/react';
import { MemoryRouter, Routes, Route, useParams } from 'react-router-dom';
import { TeamsListPage } from '../TeamsListPage';
import { OrganizationsPage } from '../OrganizationsPage';
import type { Organization, Team } from '../../api/types';

vi.mock('../../i18n', () => ({
  useT: () => (key: string) => key,
  useI18n: () => ({ locale: 'en', setLocale: vi.fn(), t: (k: string) => k }),
}));

// Non-resolving so the `.then(...)` state updates (close modal / clear confirm)
// don't fire after the assertion — we only assert the mutation was invoked.
const { createTeamMutate, deleteTeamMutate, createOrgMutate, deleteOrgMutate } = vi.hoisted(() => ({
  createTeamMutate: vi.fn(() => new Promise<void>(() => {})),
  deleteTeamMutate: vi.fn(() => new Promise<void>(() => {})),
  createOrgMutate: vi.fn(() => new Promise<void>(() => {})),
  deleteOrgMutate: vi.fn(() => new Promise<void>(() => {})),
}));

vi.mock('../../hooks/queries', () => ({
  useTeams: vi.fn(),
  useCreateTeam: () => ({ mutateAsync: createTeamMutate, isPending: false, isError: false, error: null }),
  useDeleteTeam: () => ({ mutateAsync: deleteTeamMutate, isPending: false }),
  useOrganizations: vi.fn(),
  useCreateOrganization: () => ({ mutateAsync: createOrgMutate, isPending: false, isError: false, error: null }),
  useDeleteOrganization: () => ({ mutateAsync: deleteOrgMutate, isPending: false }),
}));

import * as queries from '../../hooks/queries';

const mq = queries as unknown as {
  useTeams: ReturnType<typeof vi.fn>;
  useOrganizations: ReturnType<typeof vi.fn>;
};

// ---- fixtures --------------------------------------------------------------

function team(id: string, name: string, slug: string): Team {
  return { id, org_id: 'org-1', name, slug, created_at: '', updated_at: '' };
}

function org(id: string, name: string, slug: string): Organization {
  return { id, name, slug, created_at: '', updated_at: '' } as Organization;
}

function teamsSuccess(teams: Team[]) {
  return { data: { teams }, isLoading: false, isError: false, error: null, refetch: vi.fn() };
}

function orgsSuccess(organizations: Organization[]) {
  return { data: { organizations }, isLoading: false, isError: false, error: null, refetch: vi.fn() };
}

function renderTeamsList() {
  return render(
    <MemoryRouter initialEntries={['/platform/orgs/org-1/teams']}>
      <Routes>
        <Route path="/platform/orgs/:orgId/teams" element={<TeamsListPage />} />
        <Route path="/platform/orgs/:orgId/teams/:teamId" element={<div>TEAM_DETAIL</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

function TeamsRouteProbe() {
  const { orgId } = useParams<{ orgId: string }>();
  return <div>TEAMS_LIST_ROUTE:{orgId}</div>;
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ---------------------------------------------------------------------------
// TeamsListPage
// ---------------------------------------------------------------------------

describe('TeamsListPage', () => {
  it('lists the teams returned by useTeams, each linking to its detail', () => {
    mq.useTeams.mockReturnValue(teamsSuccess([team('t1', 'Platform', 'platform'), team('t2', 'ML', 'ml')]));

    renderTeamsList();

    expect(screen.getByRole('link', { name: 'Platform' })).toHaveAttribute(
      'href',
      '/platform/orgs/org-1/teams/t1',
    );
    expect(screen.getByRole('link', { name: 'ML' })).toHaveAttribute(
      'href',
      '/platform/orgs/org-1/teams/t2',
    );
  });

  it('shows the empty state when there are no teams', () => {
    mq.useTeams.mockReturnValue(teamsSuccess([]));
    renderTeamsList();
    expect(screen.getByText('platform.teams.noTeams')).toBeInTheDocument();
  });

  it('create action calls useCreateTeam with the entered name + derived slug', () => {
    mq.useTeams.mockReturnValue(teamsSuccess([]));
    renderTeamsList();

    // Open the create modal (the page-header action button).
    fireEvent.click(screen.getByRole('button', { name: 'platform.teams.createTeam' }));

    const dialog = screen.getByRole('dialog');
    fireEvent.change(screen.getByPlaceholderText('Platform Engineering'), {
      target: { value: 'Data Science' },
    });
    // Submit inside the modal (there are now two buttons with this label).
    fireEvent.click(within(dialog).getByRole('button', { name: 'platform.teams.createTeam' }));

    expect(createTeamMutate).toHaveBeenCalledTimes(1);
    expect(createTeamMutate).toHaveBeenCalledWith({
      name: 'Data Science',
      slug: 'data-science',
      description: undefined,
    });
  });

  it('delete is confirm-first: one click arms, second click calls useDeleteTeam', () => {
    mq.useTeams.mockReturnValue(teamsSuccess([team('t1', 'Platform', 'platform')]));
    renderTeamsList();

    const del = screen.getByRole('button', { name: 'platform.teams.delete' });
    fireEvent.click(del);
    expect(deleteTeamMutate).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'platform.teams.delete' }));
    expect(deleteTeamMutate).toHaveBeenCalledWith('t1');
  });
});

// ---------------------------------------------------------------------------
// OrganizationsPage → "View Teams" navigation target exists
// ---------------------------------------------------------------------------

describe('OrganizationsPage "View Teams" link', () => {
  it('navigates to the teams-list route (which exists), not NotFound', () => {
    mq.useOrganizations.mockReturnValue(orgsSuccess([org('org-1', 'Acme', 'acme')]));

    render(
      <MemoryRouter initialEntries={['/platform/orgs']}>
        <Routes>
          <Route path="/platform/orgs" element={<OrganizationsPage />} />
          <Route path="/platform/orgs/:orgId/teams" element={<TeamsRouteProbe />} />
          <Route path="*" element={<div>NOT_FOUND</div>} />
        </Routes>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole('button', { name: 'platform.orgs.viewTeams' }));

    expect(screen.getByText('TEAMS_LIST_ROUTE:org-1')).toBeInTheDocument();
    expect(screen.queryByText('NOT_FOUND')).not.toBeInTheDocument();
  });
});
