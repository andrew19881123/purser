# Backup & Restore

## Overview

Purser stores all state (model registry, active deployments, API keys, audit
log, PKI certificates) in a single SQLite database. Regular backups protect
against data loss and support disaster-recovery testing required by DORA
Article 12.

Backups are performed **online** using SQLite's `VACUUM INTO` statement, which
takes a consistent read-only snapshot without pausing in-flight writes or
blocking readers for more than a few milliseconds.

---

## Quick backup

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

---

## Automated backup (systemd timer / cron)

### cron

```cron
# /etc/cron.d/purser-backup
# Daily at 02:00, keep 30 days of files
0 2 * * * purser /usr/local/bin/control-plane backup \
  --output /backup/purser-$(date +\%F).db
# Prune files older than 30 days
5 2 * * * purser find /backup -name 'purser-*.db' -mtime +30 -delete
```

### systemd timer

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

---

## Kubernetes CronJob

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

## Restore procedure

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
