# Platform Organizations & Teams API

Purser v0.4 introduces a multi-tenant platform model with **Organizations**, **Teams**, and membership management. These endpoints allow operators to create and manage the organizational hierarchy that controls access to GPU resources.

> **Note:** Full RBAC (org-scoped roles, team-scoped roles, permission inheritance) is planned for a future release. In v0.4, all mutating operations require a platform **admin** API key.

---

## Authentication

All endpoints require a `Bearer` API key:

```
Authorization: Bearer <api-key>
```

Admin keys may perform all operations. Viewer keys may only call `GET` endpoints.

---

## Organizations

### Create Organization

```
POST /api/v1/platform/orgs
```

**Requires:** platform admin.

**Request body:**

```json
{
  "name": "Acme Corp",
  "slug": "acme",
  "description": "Optional description"
}
```

Both `name` and `slug` are required. The slug must be unique across the platform.

**Response `201`:**

```json
{
  "id": "org-a1b2c3d4",
  "name": "Acme Corp",
  "slug": "acme",
  "description": "Optional description",
  "created_at": "2026-09-06T00:00:00Z",
  "updated_at": "2026-09-06T00:00:00Z"
}
```

**Errors:**

| Code | `error`   | Reason                     |
|------|-----------|----------------------------|
| 400  | `bad_request` | Missing `name` or `slug` |
| 403  | `forbidden`   | Not platform admin       |
| 409  | `conflict`    | Slug already in use      |

**curl example:**

```bash
curl -s -X POST https://purser.example.com/api/v1/platform/orgs \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"Acme Corp","slug":"acme"}'
```

---

### List Organizations

```
GET /api/v1/platform/orgs
```

Returns all organizations. Admin sees all; future releases will scope this to the caller's memberships.

**Response `200`:**

```json
{
  "organizations": [
    { "id": "org-a1b2c3d4", "name": "Acme Corp", "slug": "acme", ... }
  ]
}
```

---

### Get Organization

```
GET /api/v1/platform/orgs/{id}
```

**Response `200`:** Organization object. **`404`** when not found.

**curl example:**

```bash
curl -s https://purser.example.com/api/v1/platform/orgs/org-a1b2c3d4 \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY"
```

---

### Update Organization

```
PUT /api/v1/platform/orgs/{id}
```

**Requires:** platform admin (a future release will add org_admin).

Updates `name` and/or `description`. Omit a field to leave it unchanged.

**Request body:**

```json
{
  "name": "New Name",
  "description": "Updated description"
}
```

**Response `200`:** Updated Organization object.

---

### Delete Organization

```
DELETE /api/v1/platform/orgs/{id}
```

**Requires:** platform admin.

Refuses deletion when the organization still has teams (**409 Conflict**). Delete all teams first.

**Errors:**

| Code | `error`        | Reason                         |
|------|----------------|--------------------------------|
| 403  | `forbidden`    | Not platform admin             |
| 404  | `not_found`    | Organization does not exist    |
| 409  | `org_has_teams`| Organization has existing teams|

---

## Teams

### Create Team

```
POST /api/v1/platform/orgs/{orgId}/teams
```

**Requires:** platform admin (future: org_admin).

**Request body:**

```json
{
  "name": "ML Research",
  "slug": "ml-research",
  "description": "Machine learning research team"
}
```

**Response `201`:**

```json
{
  "id": "team-e5f6g7h8",
  "org_id": "org-a1b2c3d4",
  "name": "ML Research",
  "slug": "ml-research",
  "description": "Machine learning research team",
  "created_at": "2026-09-06T00:00:00Z",
  "updated_at": "2026-09-06T00:00:00Z"
}
```

**Errors:**

| Code | `error`    | Reason                               |
|------|------------|--------------------------------------|
| 400  | `bad_request` | Missing `name` or `slug`          |
| 403  | `forbidden`   | Not authorized                    |
| 404  | `not_found`   | Organization not found            |
| 409  | `conflict`    | Slug already in use in this org   |

**curl example:**

```bash
curl -s -X POST https://purser.example.com/api/v1/platform/orgs/org-a1b2c3d4/teams \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"ML Research","slug":"ml-research"}'
```

---

### List Teams in Organization

```
GET /api/v1/platform/orgs/{orgId}/teams
```

**Response `200`:**

```json
{
  "teams": [
    { "id": "team-e5f6g7h8", "org_id": "org-a1b2c3d4", "name": "ML Research", ... }
  ]
}
```

---

### Get Team

```
GET /api/v1/platform/teams/{id}
```

**Response `200`:** Team object. **`404`** when not found.

---

### Update Team

```
PUT /api/v1/platform/teams/{id}
```

**Requires:** platform admin (future: org_admin, team_admin).

Updates `name` and/or `description`.

**Response `200`:** Updated Team object.

---

### Delete Team

```
DELETE /api/v1/platform/teams/{id}
```

**Requires:** platform admin (future: org_admin).

**Response `204` No Content** on success.

---

## Organization Members

### Add Member to Organization

```
POST /api/v1/platform/orgs/{orgId}/members
```

**Requires:** platform admin.

The `user_id` must correspond to an existing platform user (created on first OIDC/LDAP login via `UpsertPlatformUser`).

**Request body:**

```json
{
  "user_id": "oidc-sub-or-ldap-dn",
  "role": "member"
}
```

`role` must be `"org_admin"` or `"member"` (default: `"member"`).

**Response `201`:**

```json
{
  "id": 1,
  "org_id": "org-a1b2c3d4",
  "user_id": "oidc-sub-or-ldap-dn",
  "role": "member",
  "invited_by": "apikey:deadbeef",
  "created_at": "2026-09-06T00:00:00Z"
}
```

**curl example:**

```bash
curl -s -X POST https://purser.example.com/api/v1/platform/orgs/org-a1b2c3d4/members \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"sub|12345","role":"member"}'
```

---

### List Organization Members

```
GET /api/v1/platform/orgs/{orgId}/members
```

**Response `200`:**

```json
{
  "members": [
    { "id": 1, "org_id": "org-a1b2c3d4", "user_id": "sub|12345", "role": "member", ... }
  ]
}
```

---

### Update Organization Member Role

```
PUT /api/v1/platform/orgs/{orgId}/members/{userId}
```

**Requires:** platform admin.

**Request body:**

```json
{
  "role": "org_admin"
}
```

**Response `200`:** Updated OrgMember object.

---

### Remove Member from Organization

```
DELETE /api/v1/platform/orgs/{orgId}/members/{userId}
```

**Requires:** platform admin.

**Response `204` No Content** on success.

---

## Team Members

### Add Member to Team

```
POST /api/v1/platform/teams/{teamId}/members
```

**Requires:** platform admin (future: team_admin, org_admin).

**Request body:**

```json
{
  "user_id": "oidc-sub-or-ldap-dn",
  "role_id": "developer"
}
```

`role_id` references a custom role defined in the organization (e.g. `"developer"`, `"viewer"`). Defaults to `"member"` if omitted.

**Response `201`:**

```json
{
  "id": 1,
  "team_id": "team-e5f6g7h8",
  "user_id": "oidc-sub-or-ldap-dn",
  "role_id": "developer",
  "invited_by": "apikey:deadbeef",
  "created_at": "2026-09-06T00:00:00Z"
}
```

**curl example:**

```bash
curl -s -X POST https://purser.example.com/api/v1/platform/teams/team-e5f6g7h8/members \
  -H "Authorization: Bearer $PURSER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"sub|12345","role_id":"developer"}'
```

---

### List Team Members

```
GET /api/v1/platform/teams/{teamId}/members
```

**Response `200`:**

```json
{
  "members": [
    { "id": 1, "team_id": "team-e5f6g7h8", "user_id": "sub|12345", "role_id": "developer", ... }
  ]
}
```

---

### Update Team Member Role

```
PUT /api/v1/platform/teams/{teamId}/members/{userId}
```

**Requires:** platform admin.

**Request body:**

```json
{
  "role_id": "reviewer"
}
```

**Response `200`:** Updated TeamMember object.

---

### Remove Member from Team

```
DELETE /api/v1/platform/teams/{teamId}/members/{userId}
```

**Requires:** platform admin.

**Response `204` No Content** on success.

---

## Permission Notes (v0.4)

In v0.4, all mutating endpoints require the platform **admin** API key role. The authorization model will be expanded in a future release to support:

- `org_admin` — full control over a single organization and its teams
- `team_admin` — manage members and resources within a single team
- `member` — read access scoped to the team's resources

Use a platform admin key for all management operations until the expanded authorization model ships.

---

## Common Error Responses

All errors follow the standard shape:

```json
{
  "error": "error_code",
  "message": "Human-readable description"
}
```

| HTTP | `error`         | Meaning                                  |
|------|-----------------|------------------------------------------|
| 400  | `bad_request`   | Missing or invalid request fields        |
| 403  | `forbidden`     | Insufficient role for this operation     |
| 404  | `not_found`     | Entity does not exist                    |
| 409  | `conflict`      | Slug conflict or referential constraint  |
| 500  | `*_failed`      | Internal error (logged server-side)      |

---

## See also

- [Platform Users & Custom Roles](platform-users.md) — manage users, custom roles, and permissions
- [RBAC Permissions](../configuration/permissions.md) — full permission reference and built-in roles
- [Platform model overview](../configuration/platform-model.md) — how organizations, teams, and API keys relate
- [API Key Lifecycle](../configuration/api-keys.md) — create admin keys for managing organizations
