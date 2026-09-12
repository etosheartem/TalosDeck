# Worker replacement

Worker replacement is a cluster-scoped administrator operation under **Operations → Worker replacement**. It is limited to workers created and registered by TalosDeck. Imported machines without an ownership record and control-plane nodes are excluded.

Worker replacement requires a configured independent execution authority; see [management recovery](management-recovery.md) for SSH authority setup. A legacy installation without this authority can still display plans, but cannot execute replacement. Authority is checked before each protected infrastructure mutation.

The supported fencing strategy is **permanent deletion of the owned Proxmox VM**. Power-off alone is not fencing. The operation does not migrate local disks or restore persistent workload data.

## Plan and approve

1. Select the old registered worker and choose reachable or unavailable mode. Kubernetes identity and readiness must agree with that selection.
2. Configure exactly one replacement worker using the same provider, a new node name, and a different static address if applicable. Choose an Image Factory profile or a prepared ISO and matching installer.
3. Review the plan's pinned Kubernetes Node UID, replacement configuration, and workload/storage impact.
4. Review affected pods, PVC/PV identities, reclaim policy, access modes, CSI drivers, VolumeAttachments, node affinity, topology, and local paths. Incomplete inventory blocks planning or execution.
5. Acknowledge storage impact and permanent VM deletion, then enter the old node's name. Editing the form invalidates its approval. Plans expire; execution repeats safety checks.

A PVC's existence does not prove its contents can be recovered or attached to another machine. Drain does not move local data. Review any database recovery and external storage procedure separately before approving removal.

## Execution and fencing

In reachable mode, the workflow drains the old worker and waits for the replacement to become Ready before deleting the old VM and Kubernetes Node record. In unavailable mode, it verifies impact, destroys the old VM, removes its stale Node record, and creates the replacement.

Proxmox fencing verifies the stored ownership marker, machine UUID, name and MAC before mutation. HA-managed machines are blocked. Successful fencing requires a completed deletion task and authoritative absence of the VMID: TalosDeck checks effective `VM.Audit` permission for `/vms/<vmid>` before and after reading cluster-wide VM inventory, and verifies the HA resource is absent. HA inspection requires `Sys.Audit` at `/`.

Fencing evidence expires after 60 seconds and must be refreshed before deleting the stale Kubernetes Node. Refreshing evidence only observes the provider; it never repeats deletion. An unrelated machine reusing the VMID, missing permissions, provider outage, or an unconfirmed task prevents continuation. After the old VM has been fenced and its pinned Kubernetes Node UID removed, the provider may allocate that VMID to the recorded replacement child. Only that exact child, with its distinct verified ownership identity and matching provisioning plan, is accepted; the old deletion evidence is retained. Kubernetes deletion is bound to the pinned Node UID.

Completion also verifies affected controllers by their pinned UID and observed generation, desired readiness, and actual Ready pods. DaemonSets need a Ready pod on the replacement node. Missing controller identities, unsupported controller types, unhealthy or unscheduled pods, and changed DaemonSet placement require review; a Ready node alone is insufficient. This checks Kubernetes readiness, not application data integrity.

TalosDeck does not assume that an unreachable machine is stopped. Provider API errors are not evidence of deletion. The fencing proof cannot prevent an administrator from independently recreating infrastructure after the observation; changes in identity must be treated as a new review.

## Interrupted operations

Use Jobs to inspect persisted intent, evidence and the last verified step. An interrupted operation does not restart commands automatically.

Replacement history can request **Reconcile and plan continuation**. This creates a new approval plan ID after checking the retained child provisioning plan and old machine identity. It does not itself execute the continuation. Before submitting the continuation, the operator must also inspect and explicitly acknowledge the original interrupted replacement job in Jobs. Its execution lock is not cleared automatically. Then review and approve the new plan. Unsupported or ambiguous intermediate states remain blocked rather than allocating another replacement.

The current state machine uses `workflowVersion: 1`. Continuation is supported only when exactly one recorded child VM already has `config-applied` or `ready` state and its ownership can be verified. It observes that existing VM and waits for readiness; it never creates a second VM or reapplies MachineConfig. Interruptions before this durable child configuration boundary, ambiguous configuration delivery, unsupported versions, and missing historical workload/controller identities require operator review. Plans remain readable for diagnosis. A new storage preview cannot replace the original workload recovery obligations.

Before execution, affected workloads must have pinned controller identities supported by the readiness verifier: ReplicaSet/Deployment, StatefulSet, or DaemonSet. Job, static, and unmanaged pods require a separate reviewed recovery procedure; acknowledging storage risk does not bypass this restriction. Readiness verification does not prove database consistency or PVC data restoration.

Management recovery safe mode blocks replacement execution. Controlled activation enables manual operations; automatic schedules can remain paused independently.

## API

All routes require an explicitly selected cluster and administrator authorization:

- `GET /api/clusters/:cluster/replacements`
- `POST /api/clusters/:cluster/replacements/plan`
- `POST /api/clusters/:cluster/replacements`
- `POST /api/clusters/:cluster/replacements/:id/resume-plan`

Approval includes the plan ID, old node name, impact hash, and explicit storage acknowledgement. Clients cannot submit provider evidence or substitute a different machine during continuation.


## Lab acceptance — 2026-09-13

The supported Proxmox workflow was exercised against Talos 1.14.0 and Kubernetes
1.37.0 using dedicated, TalosDeck-owned test workers:

- **Unavailable worker (C2):** stopped worker became NotReady; the workflow destroyed
  its owned VM, proved fencing, removed the pinned Node UID and brought its replacement
  to Ready. Affected DaemonSets passed readiness verification.
- **Crash and continuation (final C4):** TalosDeck was killed with SIGKILL after
  ApplyConfig, before the replacement Node's Kubernetes creation timestamp. Restart
  retained an interrupted job; observation and explicit review preceded a new approval
  ID. Continuation used the same VMID and machine UUID without another create, boot or
  ApplyConfig. The original approval and duplicate submission were rejected.
- **Workloads across drain:** a stateless ReplicaSet initially ran on the old worker.
  Its pod moved to another worker during drain and disappeared from the refreshed
  node impact. Offline inspection of the encrypted durable plans proved that
  `OriginalImpact` remained unchanged in both the original and completed continuation.
  Completion verified that original controller UID, its new Ready pod and the DaemonSets.
- **Provider outage at execution preflight:** the Proxmox transport was disconnected
  after planning. The plan required review without entering drain, fencing or VM creation;
  owned inventory remained unchanged. Restoring the transport did not resume the job;
  an explicitly confirmed new plan was needed.

The provider-outage run does not prove every mid-delete or in-flight failure window.
This lab exercised stateless workloads, not PVC data restoration or application data
consistency. Unit/race checks cover additional identity, storage and fencing failures;
these do not substitute for the separate authority-failure acceptance of TD-31.
All temporary workers and the workload namespace were removed afterwards; the three
original cluster nodes remained Ready.
