# Management-plane recovery

This workflow protects **TalosDeck's own state**, separately from etcd and workload backups.
It is an offline recovery foundation. Full TD-30/31 acceptance, executor fencing and
live C1/C8/C9 are not yet certified. Restored installations remain in safe mode;
there is intentionally no web action that blindly resumes interrupted operations.

## Independent storage and keys

Keep the master **keyring** outside the management VM and outside its backup archive.
Retain historical keys after rotation: older archives need their original key ID.
Keep the target configuration, upload receipts and deployment configuration separately.
A backup and its key on the same lost disk do not provide disaster recovery.

The archive includes files under DATA (SQLite, jobs, journals and local backups),
excluding encryption keys, locks and the previous recovery marker. The entire archive
is encrypted and authenticated; extraction verifies paths, schema and file checksums.
External legacy job/backup directories must first be consolidated into DATA. Environment
and deployment secrets outside DATA are **not** automatically captured. Preserve their
independent recovery configuration; do not put them in a public repository.

An off-host target is required by the recovery CLI. HTTPS S3 example:

```json
{"type":"s3","endpoint":"https://backup.example.net","bucket":"talosdeck-dr","region":"us-east-1","accessKey":"REPLACE","secretKey":"REPLACE"}
```

SSH example (system SSH client and known host key required):

```json
{"type":"ssh","host":"backup-host","directory":"/srv/backups/talosdeck"}
```

Protect this JSON and SSH credentials. Verify the target's disk and failure domain
are independent of the management VM; a remote-looking URL alone cannot prove this.

## Create and restore

Stop TalosDeck cleanly before backup. A running database lock or a hot journal blocks
backup. Scheduled backup wrappers must stop/start the service reliably; do not kill it
mid-write to obtain a backup.

```sh
talosdeck recovery backup --data /srv/talosdeck/data \
  --key /secure/talosdeck/master.key --target /secure/dr-target.json \
  --receipt /secure/receipts/backup-2026-09-13.json
```

The command uploads a uniquely named encrypted archive and reads it back to verify its
checksum. Keep the receipt independently. A timeout is not proof of failed upload;
inspect the target for an orphaned object before cleanup.

On a clean host, install a compatible TalosDeck binary and supply the keyring and
independent target configuration. The restore destination must not exist:

```sh
talosdeck recovery restore --data /srv/talosdeck/restored \
  --key /secure/talosdeck/master.key --target /secure/dr-target.json \
  --receipt /secure/receipts/backup-2026-09-13.json

talosdeck --data /srv/talosdeck/restored --encryption-key /secure/talosdeck/master.key
```

Validation and supported SQLite migrations happen in a staging directory before atomic
publication. A wrong key, corrupt archive, incompatible schema or existing destination
fails closed. Original backup bytes remain unchanged.

`GET /api/recovery/status` reports safe mode. Infrastructure mutations return HTTP 423;
WebSocket transports, schedulers, background deliveries and collectors are disabled.
Authentication and ordinary HTTP reads remain available. Removing the marker does not
unlock a running process. **Do not remove it to bypass recovery review.** Before any
future activation, fence the previous executor, reconcile unknown outcomes and prove
exclusive execution authority. Safe mode on the new host does not fence an old host.

## Scheduled restore drill and recovery objectives

Run this read-only command periodically from a machine with independently supplied keys:

```sh
talosdeck recovery drill --key /secure/talosdeck/master.key \
  --target /secure/dr-target.json --receipt /secure/receipts/backup-2026-09-13.json
```

It downloads, decrypts, validates integrity/schema and performs supported migrations in
an isolated temporary directory. It never starts TalosDeck or contacts managed clusters.
Schedule with the host's systemd timer/cron and retain timestamped results; nonzero exit
must alert the operator through their monitoring. Do not overwrite an earlier successful
report when a drill fails. Drill time never replaces backup creation time or resets RPO.

Define a backup interval, retention and target RPO/RTO before production use. Measure
RPO against the last recoverable off-host snapshot, and RTO from incident declaration
through restored authentication, cluster/provider reconnection and execution review.
CLI validation duration is only part of RTO; it is not a production recovery claim.

Example units are in `deploy/systemd/talosdeck-restore-drill.{service,timer}`.
Create the dedicated `talosdeck-drill` account, make `/etc/talosdeck-drill` readable only
by it, and provision keyring/target/receipt independently. For SSH, provision a dedicated
known_hosts and identity through system SSH configuration accessible to that account
(the example has ProtectHome enabled). Install both units, then run:

```sh
systemctl daemon-reload
systemctl start talosdeck-restore-drill.service
systemctl enable --now talosdeck-restore-drill.timer
journalctl -u talosdeck-restore-drill.service
```

The timer defaults to daily; change OnCalendar for your policy. Persistent catch-up is
safe here because the drill is isolated and never executes infrastructure operations.
Forward the journal and failed-unit status to independent monitoring. Updating
`latest-receipt.json` is a backup publication step, never a restore-drill step.
