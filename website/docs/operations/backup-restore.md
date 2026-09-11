# Backup & Restore

## Overview

Purser stores all state (model registry, active deployments, API keys, audit
log, PKI certificates) in its database — **PostgreSQL** in production
deployments and **SQLite** for development and single-node setups. Regular
backups protect against data loss and support disaster-recovery testing required
by DORA Article 12.

| Database | Default in | Backup tool |
|---|---|---|
| PostgreSQL (`PURSER_DB_DRIVER=postgres`) | Helm chart, docker-compose production | `pg_dump` / `pg_restore` |
| SQLite (`PURSER_DB_DRIVER=sqlite`) | Development, single-node | `control-plane backup` (`VACUUM INTO`) |

Jump to the section for your database:
- [PostgreSQL backup](#postgresql-backup) / [PostgreSQL restore](#postgresql-restore)
- [SQLite backup](#sqlite-backup) / [SQLite restore](#sqlite-restore)

---

## PostgreSQL backup

### One-off dump

Use `pg_dump` with the custom format (`-Fc`) for maximum flexibility: it is
compressed, supports parallel restore, and allows selective object restore.

```bash
pg_dump -Fc -h $PGHOST -U purser purser \
  > purser-$(date +%Y%m%d-%H%M).dump
```

**Authentication** — supply the password with one of:

- `PGPASSWORD` environment variable (suitable for scripts; not visible in `ps`
  on modern Linux):
  ```bash
  PGPASSWORD=secret pg_dump -Fc -h $PGHOST -U purser purser \
    > purser-$(date +%Y%m%d-%H%M).dump
  ```
- `~/.pgpass` file (preferred for interactive use and systemd units):
  ```
  # hostname:port:database:username:password
  db.internal:5432:purser:purser:secret
  ```
  Set permissions: `chmod 0600 ~/.pgpass`

**Custom vs. plain SQL format:**

| Flag | Output | Use when |
|---|---|---|
| `-Fc` (custom) | Binary, compressed, splittable | Default — use with `pg_restore` |
| `-F p` (plain) | SQL text | Pipe into `psql`, human-readable inspection |
| `-F d` (directory) | One file per table | Parallel restore with `pg_restore -j N` |

The custom format (`-Fc`) is recommended for all automated backups.

### Continuous backup (WAL archiving)

For production, point-in-time recovery (PITR) via WAL archiving eliminates the
gap between daily dumps:

- **[pgBackRest](https://pgbackrest.org/)** — full/differential/incremental
  backup, WAL archiving, parallel restore. Recommended for self-managed
  PostgreSQL.
- **[WAL-G](https://github.com/wal-g/wal-g)** — lightweight WAL archiver to
  S3/GCS/Azure. Drop in a `archive_command` one-liner.
- **Managed services** (AWS RDS, Cloud SQL, Neon, Supabase) — enable automated
  backups and PITR from the provider console; `pg_dump` is still useful for
  logical, database-level portability.

### Automated backup — cron

```cron
# /etc/cron.d/purser-pg-backup
# Daily at 02:00, keep 30 days
0 2 * * * purser PGPASSWORD=secret pg_dump -Fc \
  -h $PGHOST -U purser purser \
  -f /backup/purser-$(date +\%Y\%m\%d-\%H\%M).dump
5 2 * * * purser find /backup -name 'purser-*.dump' -mtime +30 -delete
```

### Automated backup — systemd timer

`/etc/systemd/system/purser-pg-backup.service`:

```ini
[Unit]
Description=Purser PostgreSQL daily backup

[Service]
Type=oneshot
User=purser
Environment=PGHOST=db.internal
Environment=PGPASSWORD=secret
ExecStart=/usr/bin/pg_dump -Fc -U purser purser \
  -f /backup/purser-%I.dump
```

`/etc/systemd/system/purser-pg-backup.timer`:

```ini
[Unit]
Description=Daily Purser PostgreSQL backup

[Timer]
OnCalendar=daily
AccuracySec=1min
Persistent=true

[Install]
WantedBy=timers.target
```

Enable with `systemctl enable --now purser-pg-backup.timer`.

### Kubernetes CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: purser-pg-backup
  namespace: purser
spec:
  schedule: "0 2 * * *"
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: pg-backup
              image: postgres:16-alpine
              command:
                - /bin/sh
                - -c
                - |
                  pg_dump -Fc -h $PGHOST -U $PGUSER $PGDATABASE \
                    -f /backup/purser-$(date +%Y%m%d-%H%M).dump
              env:
                - name: PGHOST
                  valueFrom:
                    secretKeyRef:
                      name: purser-db
                      key: host
                - name: PGUSER
                  value: purser
                - name: PGDATABASE
                  value: purser
                - name: PGPASSWORD
                  valueFrom:
                    secretKeyRef:
                      name: purser-db
                      key: password
              volumeMounts:
                - name: backup
                  mountPath: /backup
          volumes:
            - name: backup
              persistentVolumeClaim:
                claimName: purser-pg-backup
```

---

## PostgreSQL restore

> **Stop the control plane before restoring.** Active connections hold locks
> that conflict with `pg_restore`; restoring while the service is running risks
> partial writes and data corruption.

```bash
# 1. Stop the service (systemd)
systemctl stop purser-control-plane

# 2. Drop and recreate the target database
psql -h $PGHOST -U purser postgres \
  -c "DROP DATABASE IF EXISTS purser;" \
  -c "CREATE DATABASE purser OWNER purser;"

# 3. Restore from the custom-format dump
pg_restore -Fc -h $PGHOST -U purser -d purser \
  purser-20260906-0200.dump

# 4. Restart the service
systemctl start purser-control-plane

# 5. Verify
control-plane status
```

For parallel restore (faster on large databases):

```bash
pg_restore -Fc -h $PGHOST -U purser -d purser -j 4 \
  purser-20260906-0200.dump
```

### Point-in-time recovery (WAL archiving)

If you use WAL archiving (pgBackRest / WAL-G), restore to a specific moment:

```bash
# pgBackRest example — restore to 2026-09-06 03:00 UTC
pgbackrest --stanza=purser --delta \
  --target="2026-09-06 03:00:00+00" \
  --target-action=promote \
  restore
```

Consult your WAL archiver's documentation for full PITR procedures and
`recovery.conf` / `postgresql.conf` settings (`restore_command`,
`recovery_target_time`).

---

## SQLite backup

!!! note "SQLite is the development/single-node default"
    SQLite is the development/single-node default. For production deployments
    use PostgreSQL — see [PostgreSQL backup](#postgresql-backup) above.

SQLite backups are performed **online** using SQLite's `VACUUM INTO` statement,
which takes a consistent read-only snapshot without pausing in-flight writes or
blocking readers for more than a few milliseconds.

```bash
control-plane backup --db /var/lib/purser/registry.db \
  --output /backup/purser-$(date +%Y%m%d-%H%M).db
```

`--db` defaults to the `PURSER_DB` environment variable (or
`purser-registry.db` in the current directory), so in a typical deployment
only `--output` is required:

```bash
PURSER_DB=/var/lib/purser/registry.db \
  control-plane backup --output /backup/purser-$(date +%Y%m%d-%H%M).db
```

The backup file is a fully self-contained SQLite 3 database. Copy it off-site
with any standard tool (`rsync`, `scp`, S3 sync, etc.).

### Automated backup (systemd timer / cron)

#### cron

```cron
# /etc/cron.d/purser-backup
# Daily at 02:00, keep 30 days of files
0 2 * * * purser /usr/local/bin/control-plane backup \
  --output /backup/purser-$(date +\%F).db
# Prune files older than 30 days
5 2 * * * purser find /backup -name 'purser-*.db' -mtime +30 -delete
```

#### systemd timer

`/etc/systemd/system/purser-backup.service`:

```ini
[Unit]
Description=Purser control-plane daily backup

[Service]
Type=oneshot
User=purser
Environment=PURSER_DB=/var/lib/purser/registry.db
ExecStart=/usr/local/bin/control-plane backup \
  --output /backup/purser-%I.db
```

`/etc/systemd/system/purser-backup.timer`:

```ini
[Unit]
Description=Daily Purser backup

[Timer]
OnCalendar=daily
AccuracySec=1min
Persistent=true

[Install]
WantedBy=timers.target
```

Enable with `systemctl enable --now purser-backup.timer`.

### Kubernetes CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: purser-backup
  namespace: purser
spec:
  schedule: "0 2 * * *"
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: backup
              image: ghcr.io/purser/purser-control-plane:latest
              command:
                - control-plane
                - backup
                - --output
                - /backup/purser-$(date +%Y%m%d-%H%M).db
              env:
                - name: PURSER_DB
                  value: /data/registry.db
              volumeMounts:
                - name: data
                  mountPath: /data
                - name: backup
                  mountPath: /backup
          volumes:
            - name: data
              persistentVolumeClaim:
                claimName: purser-data
            - name: backup
              persistentVolumeClaim:
                claimName: purser-backup
```

---

## SQLite restore

> **Stop the control plane before restoring.** A live process holds the
> database open; restoring while it is running may corrupt the file.

```bash
# 1. Stop the service
systemctl stop purser-control-plane

# 2. Restore (--confirm required to prevent accidental overwrites)
control-plane restore \
  --input /backup/purser-20260906-0200.db \
  --db /var/lib/purser/registry.db \
  --confirm

# 3. Restart
systemctl start purser-control-plane

# 4. Verify
control-plane status
```

`RestoreDB` verifies the SQLite 3 magic header before touching the destination,
writes to a temporary file in the same directory, then performs an atomic
`rename(2)` — the database file is never left in a partially-written state.

---

## Backup contents

The backup includes:

| Table | Contents |
|---|---|
| `nodes` | Registered agent nodes and hardware profiles |
| `models` | Imported models and HuggingFace provenance |
| `deployments` | Active and historical deployments |
| `plans` | Cached DP layer-split plans |
| `api_keys` | API keys, tenants, roles, and quotas |
| `usage_log` | Per-key token usage records |
| `audit_log` | Tamper-evident audit chain |
| `certs` | Internal PKI certificates and revocations |

The backup does **not** include:

- **Model weights** — stored locally on each agent node; managed by the agent
  daemon, not the control plane.
- **External TLS certificates** — if you supply `PURSER_TLS_CERT` /
  `PURSER_TLS_KEY`, those files are outside the database; back them up
  separately.

---

## Verifying a backup

```bash
# Quick SQLite header check (should print "SQLite format 3")
head -c 16 /backup/purser-20260906-0200.db

# Row counts (human sanity check)
sqlite3 /backup/purser-20260906-0200.db \
  "SELECT 'models', COUNT(*) FROM models
   UNION ALL SELECT 'deployments', COUNT(*) FROM deployments
   UNION ALL SELECT 'api_keys', COUNT(*) FROM api_keys
   UNION ALL SELECT 'audit_log', COUNT(*) FROM audit_log;"
```

---

## DORA compliance

This backup procedure supports **DORA Article 12** (ICT backup policies)
requirements:

| DORA requirement | How Purser addresses it |
|---|---|
| Backup frequency | Configurable; recommended daily minimum |
| Backup integrity | VACUUM INTO produces a fully consistent SQLite copy; header verification before restore |
| Recovery point objective (RPO) | Equal to the backup interval; sub-daily schedules (e.g. hourly) reduce RPO to ≤ 1 hour |
| Recovery time objective (RTO) | Restore is a single command; typical time < 60 seconds for databases up to several GB |
| Backup isolation | Backup files are written outside the data volume; off-site copy (S3, rsync) recommended |
| Restore testing | Schedule a periodic restore-to-staging test; use `control-plane restore --confirm` against a non-production path |

Operators should document their specific RPO/RTO targets, backup retention
policy, and restore-test schedule in their ICT continuity plan.

---

## See also

- [Environment variables](../configuration/env-vars.md) — `PURSER_DB` and database connection options
- [PKI Operations](pki-operations.md) — renew or rotate the internal CA; PKI state is included in the backup
- [HA Control Plane](../enterprise/ha-control-plane.md) — Raft-based HA; backup procedure for multi-node clusters
- [Audit log](../enterprise/audit-log.md) — the tamper-evident `audit_log` table is part of every backup
