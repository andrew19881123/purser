// RolesPage tests — org-scoped custom-role management (v0.4 RBAC).
//
// Covers:
//   (a) the page lists roles (built-in + custom) from a mocked hook;
//   (b) create-role submits name + selected permission keys to the create hook;
//   (c) the permission multi-select renders options grouped by scope from a
//       mocked catalog;
//   (d) delete is confirm-first (first click does NOT mutate; a confirm control
//       must be clicked to fire the delete);
//   + built-in roles are read-only (no delete control).
//
// RBAC role endpoints are NOT enterprise-gated (verified against the Go control
// plane — see the report), so there is no 402 locked-panel path to assert here.
import { render, screen, fireEvent, within, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { I18nProvider } from '../../i18n';
import { RolesPage } from '../RolesPage';
import type { CustomRole, PermissionDescriptor } from '../../api/types';

// ---------------------------------------------------------------------------
// Mock hooks
// ---------------------------------------------------------------------------

vi.mock('../../hooks/queries', () => ({
  useRoles: vi.fn(),
  usePermissionCatalog: vi.fn(),
  useCreateRole: vi.fn(),
  useUpdateRole: vi.fn(),
  useDeleteRole: vi.fn(),
  useOrganizations: vi.fn(),
}));

import {
  useRoles,
  usePermissionCatalog,
  useCreateRole,
  useUpdateRole,
  useDeleteRole,
} from '../../hooks/queries';

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const SYSTEM_ROLE: CustomRole = {
  id: 'org_admin',
  orgId: '',
  name: 'Organization Administrator',
  description: 'Full access to the organization',
  permissions: ['org:teams:create', 'team:models:deploy'],
  isSystem: true,
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};

const CUSTOM_ROLE: CustomRole = {
  id: 'role-abc123',
  orgId: 'org-1',
  name: 'ML Engineer',
  description: 'Deploys models and calls inference',
  permissions: ['team:models:deploy', 'inference:call'],
  isSystem: false,
  createdAt: '2026-02-01T00:00:00Z',
  updatedAt: '2026-02-01T00:00:00Z',
};

const CATALOG: PermissionDescriptor[] = [
  { key: 'platform:orgs:create', description: 'Create a new organization', scope: 'platform' },
  { key: 'org:teams:create', description: 'Create a new team', scope: 'org' },
  { key: 'team:models:deploy', description: "Deploy a model to a team's pool", scope: 'team' },
  { key: 'team:keys:create', description: 'Issue an API key for the team', scope: 'team' },
  { key: 'inference:call', description: 'Send requests to the gateway', scope: 'inference' },
];

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function qr(overrides: Record<string, unknown> = {}): any {
  return {
    data: undefined,
    isLoading: false,
    isError: false,
    error: null,
    isFetching: false,
    refetch: vi.fn(),
    ...overrides,
  };
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function mut(overrides: Record<string, unknown> = {}): any {
  return {
    mutate: vi.fn(),
    isPending: false,
    isError: false,
    error: null,
    ...overrides,
  };
}

function mockAll(
  roles: CustomRole[] = [],
  opts: { create?: ReturnType<typeof mut>; del?: ReturnType<typeof mut>; update?: ReturnType<typeof mut> } = {},
) {
  vi.mocked(useRoles).mockReturnValue(qr({ data: { roles } }));
  vi.mocked(usePermissionCatalog).mockReturnValue(qr({ data: { permissions: CATALOG } }));
  vi.mocked(useCreateRole).mockReturnValue(opts.create ?? mut());
  vi.mocked(useUpdateRole).mockReturnValue(opts.update ?? mut());
  vi.mocked(useDeleteRole).mockReturnValue(opts.del ?? mut());
}

function renderPage(orgId = 'org-1') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/platform/orgs/${orgId}/roles`]}>
        <I18nProvider>
          <Routes>
            <Route path="/platform/orgs/:orgId/roles" element={<RolesPage />} />
          </Routes>
        </I18nProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ---------------------------------------------------------------------------
// (a) lists roles
// ---------------------------------------------------------------------------

describe('RolesPage — role list', () => {
  it('lists both built-in and custom roles from the hook', () => {
    mockAll([SYSTEM_ROLE, CUSTOM_ROLE]);
    renderPage();
    expect(screen.getByText('Organization Administrator')).toBeInTheDocument();
    expect(screen.getByText('ML Engineer')).toBeInTheDocument();
  });

  it('marks built-in roles read-only (no Delete control)', () => {
    mockAll([SYSTEM_ROLE]);
    renderPage();
    // "Built-in" type badge is shown…
    expect(screen.getByText('Built-in')).toBeInTheDocument();
    // …and there is no Delete button for a system role.
    expect(screen.queryByRole('button', { name: /^delete$/i })).not.toBeInTheDocument();
  });

  it('renders an empty state when there are no roles', () => {
    mockAll([]);
    renderPage();
    expect(screen.getByText(/no custom roles yet/i)).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// (c) permission multi-select grouped by scope
// ---------------------------------------------------------------------------

describe('RolesPage — permission multi-select', () => {
  it('groups catalog permissions by scope in the create modal', () => {
    mockAll([]);
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Create role' }));

    const dialog = screen.getByRole('dialog');
    // One group heading per scope present in the catalog.
    expect(within(dialog).getByText('Platform')).toBeInTheDocument();
    expect(within(dialog).getByText('Organization')).toBeInTheDocument();
    expect(within(dialog).getByText('Team')).toBeInTheDocument();
    expect(within(dialog).getByText('Inference')).toBeInTheDocument();

    // Each permission renders as a checkbox keyed on its permission string.
    expect(within(dialog).getByRole('checkbox', { name: /platform:orgs:create/ })).toBeInTheDocument();
    expect(within(dialog).getByRole('checkbox', { name: /team:models:deploy/ })).toBeInTheDocument();
    expect(within(dialog).getByRole('checkbox', { name: /inference:call/ })).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// (b) create submits name + selected permission keys
// ---------------------------------------------------------------------------

describe('RolesPage — create role', () => {
  it('submits the name and the selected permission keys to the create hook', async () => {
    const createMutate = vi.fn();
    mockAll([], { create: mut({ mutate: createMutate }) });
    renderPage();

    fireEvent.click(screen.getByRole('button', { name: 'Create role' }));
    const dialog = screen.getByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Role name'), {
      target: { value: 'Deployer' },
    });
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /team:models:deploy/ }));
    fireEvent.click(within(dialog).getByRole('checkbox', { name: /inference:call/ }));

    fireEvent.click(within(dialog).getByRole('button', { name: /save role/i }));

    expect(createMutate).toHaveBeenCalledTimes(1);
    const arg = createMutate.mock.calls[0][0];
    expect(arg).toEqual(
      expect.objectContaining({ name: 'Deployer' }),
    );
    expect(arg.permissions).toEqual(
      expect.arrayContaining(['team:models:deploy', 'inference:call']),
    );
    expect(arg.permissions).toHaveLength(2);
  });

  it('keeps Save disabled until a name is entered', async () => {
    mockAll([]);
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Create role' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByRole('button', { name: /save role/i })).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('Role name'), { target: { value: 'X' } });
    await waitFor(() =>
      expect(within(dialog).getByRole('button', { name: /save role/i })).not.toBeDisabled(),
    );
  });
});

// ---------------------------------------------------------------------------
// (d) delete is confirm-first
// ---------------------------------------------------------------------------

describe('RolesPage — delete role (confirm-first)', () => {
  it('does not delete on the first click and requires a confirm', () => {
    const deleteMutate = vi.fn();
    mockAll([CUSTOM_ROLE], { del: mut({ mutate: deleteMutate }) });
    renderPage();

    // First click arms the confirm — must NOT mutate.
    fireEvent.click(screen.getByRole('button', { name: /^delete$/i }));
    expect(deleteMutate).not.toHaveBeenCalled();

    // A confirm control appears; clicking it fires the delete with the role id.
    fireEvent.click(screen.getByRole('button', { name: /confirm delete/i }));
    expect(deleteMutate).toHaveBeenCalledWith('role-abc123');
  });
});
