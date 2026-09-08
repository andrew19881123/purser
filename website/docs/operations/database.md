# Database Configuration

Purser's control plane stores its registry (nodes, models, deployments, API keys, audit
log, …) in a relational database. Two drivers are supported:

| Driver | Use case | Replicas |
|---|---|---|
| `sqlite` | Local development, single-node deployments | 1 only |
| `postgres` | Production, HA, horizontal scaling | unlimited |

!!! warning "SQLite is for development only"
    SQLite is a single-writer, embedded file database. **Do not use `replicaCount > 1`
    with SQLite** — concurrent writers will corrupt the registry. Use PostgreSQL for any
    environment that requires high availability or horizontal scaling.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `PURSER_DB_DRIVER` | `sqlite` | Database backend: `sqlite` or `postgres`. |
| `PURSER_DB_URL` | `postgres://purser:purser@localhost:5432/purser?sslmode=disable` | Full PostgreSQL DSN. Required when `PURSER_DB_DRIVER=postgres`. |
| `PURSER_DB` | `/data/purser-registry.db` | SQLite file path. Ignored when `PURSER_DB_DRIVER=postgres`. |

## PostgreSQL setup

### Requirements

- PostgreSQL 14 or later (16 recommended).
- A dedicated database and user for Purser:

```sql
CREATE USER purser WITH PASSWORD 'changeme';
CREATE DATABASE purser OWNER purser;
GRANT ALL PRIVILEGES ON DATABASE purser TO purser;
```

### Connecting

Set the two environment variables before starting the control plane:

```bash
export PURSER_DB_DRIVER=postgres
export PURSER_DB_URL="postgres://purser:changeme@db.example.com:5432/purser?sslmode=require"
```

For SSL, use `sslmode=require` (recommended) or `sslmode=verify-full` with a CA bundle
(most secure). Use `sslmode=disable` only in isolated dev environments.

### Schema migration

The control plane applies its schema on every startup (`Migrate()` is idempotent). No
separate migration tool is required. Point `PURSER_DB_URL` at an empty PostgreSQL
database and start the control plane — the tables are created automatically.

!!! note "PostgreSQL vs SQLite SQL compatibility"
    All SQL in the registry layer uses the common subset of SQL 92 supported by both
    PostgreSQL and SQLite (parameterised queries with `?` placeholders, standard DDL).
    PostgreSQL-specific syntax (e.g. `ON CONFLICT DO UPDATE`) is already in use; the
    schema has been validated on PostgreSQL 14+.

## Kubernetes (Helm)

### Using an external managed PostgreSQL (recommended for production)

RDS, Cloud SQL, Azure Database for PostgreSQL, and any other managed PostgreSQL service
all work. Supply the DSN via a Kubernetes Secret:

```bash
kubectl create secret generic purser-db \
  --from-literal=url='postgres://purser:changeme@db.example.com:5432/purser?sslmode=require'
```

Then reference it in your `values.yaml`:

```yaml
database:
  driver: postgres

controlPlane:
  extraEnv:
    - name: PURSER_DB_URL
      valueFrom:
        secretKeyRef:
          name: purser-db
          key: url
```

Install or upgrade:

```bash
helm upgrade --install purser oci://ghcr.io/andrew19881123/charts/purser \
  --version 0.4.0 \
  -f values.yaml
```

### Quick-start bundled PostgreSQL

For evaluation or non-production use, enable the bundled PostgreSQL sub-chart:

```yaml
database:
  driver: postgres
  postgresql:
    enabled: true
    auth:
      username: purser
      password: purser   # change this
      database: purser
```

!!! warning "Bundled PostgreSQL is not HA"
    The bundled PostgreSQL is a single Pod backed by a PVC. It is suitable for
    quick-start and development clusters only. For production use an external managed
    PostgreSQL with automated backups and failover.

### SQLite (single-node only)

The default `driver: sqlite` is provided for backward compatibility and development.
The chart mounts a PVC at `/data` for both the SQLite file and the internal PKI CA:

```yaml
database:
  driver: sqlite  # default; do not use replicaCount > 1 with sqlite
```

## Migrating from SQLite to PostgreSQL

If you have an existing SQLite-backed deployment and want to migrate to PostgreSQL:

1. **Stop the control plane** to ensure a clean snapshot.

2. **Dump the SQLite data** as SQL:

    ```bash
    sqlite3 /data/purser-registry.db .dump > purser-dump.sql
    ```

3. **Clean up SQLite-specific pragmas** from the dump. Remove lines starting with
   `PRAGMA` and `BEGIN EXCLUSIVE` at the top; PostgreSQL does not understand them.

4. **Import into PostgreSQL** (after creating the database and user above):

    ```bash
    psql "postgres://purser:changeme@db.example.com:5432/purser?sslmode=require" \
      < purser-dump.sql
    ```

5. **Update the control plane config** to set `PURSER_DB_DRIVER=postgres` and
   `PURSER_DB_URL` pointing at the new PostgreSQL instance.

6. **Restart the control plane**. `Migrate()` is idempotent — it runs the schema
   creation (`CREATE TABLE IF NOT EXISTS`) on the imported database without touching
   existing data.

7. **Verify** by calling `/api/v1/cluster/health` — it should return `200 ok`.

!!! tip "Test the migration in a staging environment first"
    Always dry-run the migration against a copy of your SQLite file before touching
    production data.
