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
External legacy job/backup directories must first be consolidated into DATA.
The backup command requires `--confirm-complete-data-directory`: verify the running
deployment arguments (including legacy `--backups` and `TALOSDECK_JOBS_DIR`) before
confirming. The CLI cannot discover historical deployment arguments automatically. Environment
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
talosdeck recovery backup --data /srv/talosdeck/data --confirm-complete-data-directory \
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

## Independent execution authority (opt-in foundation)

Install the same binary on an independent trusted SSH host. Its local POSIX filesystem
holds an exclusive process lease and a monotonic epoch, outside DATA and outside the
management VM backup. Do not use NFS, restore an old epoch, replace the lock file, or
force lease takeover. Restrict the SSH account/authorized command and state directory.

```sh
talosdeck --data /srv/talosdeck/data --encryption-key /secure/master.key \
  --authority-host execution-host \
  --authority-binary /usr/local/bin/talosdeck \
  --authority-state /var/lib/talosdeck-authority
```

The client starts `talosdeck authority serve --state-dir ...` through strict host-key
SSH verification. The helper holds its lock for the transport lifetime. A second
executor is refused while the first holds the lease. Each API mutation admission,
job checkpoint and persisted provider intent validates that lease. Disconnect poisons
the client permanently; it cannot silently reconnect and reuse the old epoch.
A new successful acquisition increments and fsyncs the authority's epoch.

Enabling authority is opt-in for existing installations. Once enabled, its configuration
and last acquired epoch are persisted in DATA; omitting flags on restart does not disable
it. Acquisition requires the authority epoch to match the saved expected epoch. A stale
copy is refused even after the newer executor has exited. A crash between remote grant
and local epoch persistence conservatively requires review. **An old binary without this
mechanism is not fenced by it.** All executors must
use the same authority, and superseded legacy hosts/credentials must be fenced separately.
A validation round trip also cannot retract a provider request already sent before
lease loss. Reconcile those outcomes; never treat lease loss as proof an operation failed.

Restored safe mode does not acquire execution authority and does not resume jobs.
Unavailable authority fails mutation admission closed. A live but partitioned old
session can delay takeover until its SSH session terminates; availability is sacrificed
rather than forcibly granting a second lease.

## Reviewed manual activation

After reconnecting and reviewing outcomes, stop the restored server. Use the Jobs
screen's **Reconcile** action to collect provider ownership evidence beforehand.
It is available in safe mode, never resubmits a command, and cannot prove an ambiguous
delete merely from a missing/filtered provider listing. Unproved outcomes stay UNKNOWN.

Prepare an explicit review list (empty `[]` only when there are no unresolved jobs):

```json
[{"jobId":"REPLACE-WITH-JOB-UUID","reason":"Observed the resource identity and reviewed the remaining unknown outcome; no automatic retry authorized."}]
```

Separately record a short single-line attestation explaining how the old management
instance was fenced. Do not include credentials. This is an operator assertion, not
an automatic provider fencing check. Then:

```sh
talosdeck recovery activate --data /srv/talosdeck/restored \
  --key /secure/talosdeck/master.key --actor operator-name \
  --fencing-evidence /secure/recovery/fencing.txt \
  --reviews /secure/recovery/reviews.json --confirm-old-management-fenced \
  --authority-host execution-host --authority-binary /usr/local/bin/talosdeck \
  --authority-state /var/lib/talosdeck-authority
```

The command locks the registry and job journals, validates credentials and execution
rights, records explicit job reviews without converting UNKNOWN to success, and commits
an encrypted activation receipt bound to this restore and authority epoch. The sentinel
and its encrypted database anchor remain. Removing the sentinel does not unlock the
restored database. A subsequent restore invalidates the old activation receipt.

If the expected epoch has advanced, investigate/fence the earlier executor and reconcile
its outcomes. Only after that review may an operator supply `--expected-epoch N` for the
verified current epoch. This is a compare-and-swap expectation, not forced takeover:
a live authority holder still blocks acquisition. Interrupted activation errors report
the acquired epoch for review; never guess or loop through epochs to obtain admission.

Restart with the same DATA and key. Persisted authority policy is loaded even if flags
are omitted. A fresh matching authority lease enables **manual management only**.
Backups schedules, notification delivery and other background collectors remain paused;
old jobs are not resumed. The console displays this state. Automatic scheduling cannot
yet be re-enabled through this recovery workflow; this limitation is deliberate and
must be accounted for operationally.

Lab acceptance on 2026-09-13 destroyed a dedicated management guest and disk, restored
to a fresh guest, and verified admin access, three real nodes, provider credentials and
safe mode. Measured snapshot-to-loss interval was 29.5 seconds and recovery 99.3 seconds
against declared 24h/30min lab targets. Image and binary were cached; target was on the
same hypervisor outside the guest disk. This does not prove hypervisor-loss recovery.
Separate process tests verified exclusive authority, returning stale executors (including
after the newer process exited), and that omitted flags do not bypass persisted policy.
