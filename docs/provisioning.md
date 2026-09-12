# Proxmox provisioning

TalosDeck can create a new Talos cluster, add workers to an imported cluster, and remove workers it created. Every operation is a durable background job. Provider credentials, generated machine configurations and ownership records are encrypted in the registry database.

## Prerequisites

- A Proxmox API token with permission to inspect the selected host/storage and create, configure, start and remove QEMU VMs. Use a trusted Proxmox CA certificate; TLS verification is enabled by default.
- A Talos ISO already available in Proxmox storage, such as `local:iso/talos-qemu-agent.iso`. The ISO must contain `qemu-guest-agent`, because TalosDeck discovers each new VM's address through its registered virtual NIC and guest agent.
- A bridge with DHCP for the initial maintenance boot. A requested static IPv4 configuration takes effect when MachineConfig is applied. Reserve DHCP addresses when using DHCP for control-plane endpoints.
- Connectivity from TalosDeck to Proxmox, Talos TCP 50000 and Kubernetes TCP 6443. Nodes need access to their installer and Kubernetes/CNI image registries.
- Enough host memory and storage for the selected machines. Planning and execution check total requested RAM, free space on each selected storage and the boot ISO's presence. These are capacity snapshots: other Proxmox administrators can allocate resources concurrently. TalosDeck does not delete existing VMs to make room.

Keep TalosDeck outside the cluster being created or modified.

## Connect a provider

Open **Providers**, enter the Proxmox URL, node, API token, trusted CA and default storage/ISO/bridge. Saving checks the connection and selected storage. Secret values are never returned by provider-list APIs.

Providers are independent of process environment variables. Imported clusters and new clusters can select an explicit provider without inheriting another cluster's credentials.

## Create a cluster

Open **Clusters → Create cluster** and choose the provider, stable Talos/Kubernetes versions, matching installer image and machine specifications. Supported control-plane sizes are one or three. Specify a unique DNS-label name for every machine, CPU, RAM, disk, storage, ISO, bridge and optional VLAN. Networking supports DHCP or static IPv4 with gateway and nameservers.

Choose Talos-managed Flannel or bundled Cilium 1.20.1. Cilium is limited to Kubernetes 1.33–1.36 according to its stable compatibility matrix; use Flannel for Kubernetes 1.37. Cilium uses Kubernetes IPAM and retains kube-proxy. Storage choices are none or local-path-provisioner 0.0.34. Local-path is a single-node storage provider, not replicated storage: losing the node loses its volumes. Its data directory is `/var/lib/kubelet/talosdeck-volumes`, on the node's persistent kubelet filesystem. The provisioner creates a default `local-path` StorageClass and uses a privileged helper namespace.

An explicit HTTPS Kubernetes endpoint can be supplied; otherwise the first control-plane address is used. For three control planes, arrange a stable external API endpoint/load balancer when you need endpoint redundancy. Addons are pinned manifests embedded in TalosDeck; provisioning does not download arbitrary YAML at runtime. Longhorn and custom addon charts are not bundled.

Review the plan and type its name to start. The job:

1. Persists an ownership reservation before each VM creation.
2. Creates fresh VMs with unique MAC/SMBIOS identities and ownership markers.
3. Boots the ISO and discovers addresses through the guest agent.
4. Generates fresh shared cluster credentials and validates every configuration with the version-matched Talos SDK.
5. Saves configurations encrypted, then applies them to those VMs in maintenance mode.
6. Waits for the authenticated Talos API, bootstraps etcd once and obtains kubeconfig.
7. Installs selected addons, waits for them, then verifies selected Talos/Kubernetes versions and Kubernetes Ready on every new node.
8. Imports the verified cluster into the registry.

Do not use an installer image that drops required extensions. Select an Image Factory installer containing the intended extensions and a tag matching the requested Talos release.

## Add or remove workers

Select a cluster before adding workers. TalosDeck obtains the existing cluster secrets and network settings from an authenticated control-plane MachineConfig. It does not search the management server for configuration files. New workers must use the cluster's current Kubernetes version.

Removal is limited to registered, provider-owned workers. TalosDeck verifies the VM name, MAC, SMBIOS UUID and ownership marker, matches the Kubernetes node address, verifies the current Talos worker role, and drains workloads before removing the VM. PodDisruptionBudgets remain effective. Kubernetes object cleanup uses a UID precondition to protect against same-name replacement.

Existing VMs are never adopted by a `talos-worker-*` name prefix. The old synchronous worker-create/delete API is disabled; use provisioning plans and jobs.

## Interrupted or failed jobs

Closing a browser does not stop a job. A server restart does not replay VM creation, configuration application or etcd bootstrap. An uncertain provider result requires review.

The **Machines** inventory retains reserved/created/booting/config-applied records after failures. A failed task does not automatically delete those machines or allocate replacements: the VM may already exist even if the API response was lost. Inspect the job and acknowledge its interrupted outcome. For a failed, unregistered cluster, **Cleanup** creates a separate plan and background job; type the machine name to confirm. Cleanup verifies ownership again before deletion, and records the deleted asset. It refuses running plans, completed/registered clusters and any uncertain registry import. Existing cluster workers use the drain-and-delete workflow instead.

Provider removal is refused while retained machines or active plans still reference it. Back up both the encrypted registry and its separately stored master key; neither can recover encrypted configuration without the other.

## API

Global routes: `GET/POST /api/providers`, `DELETE /api/providers/:id`, `POST /api/provision/plan`, `POST /api/provision`, `GET /api/provision/jobs`, `GET /api/machines`.

For workers, use `POST /api/clusters/:id/provision/plan`, `POST /api/clusters/:id/provision` and `GET /api/clusters/:id/machines`. Submission accepts `{ "planId": "…", "confirmedName": "…" }`; configuration and provider credentials are never copied into job requests. Plans expire after 30 minutes and cannot be replayed after execution starts.
