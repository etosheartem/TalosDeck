# Authentication and deployment

TalosDeck supports local users and OpenID Connect, including Keycloak. Every authenticated request checks the current user status and role. Passwords use bcrypt; users, signing keys and revocations are stored in the encrypted SQLite registry. Infrastructure credentials never appear in user-management responses.

## First administrator and local users

Set `TALOSDECK_ADMIN_PASSWORD` before the first startup. The initial `admin` account inherits this password, including an existing bcrypt hash from an older installation. Subsequent starts preserve database passwords; changing the environment variable does not reset a user. No generated password is printed to logs.

Administrators create users, change roles, disable accounts, reset passwords and revoke sessions from Security. New and changed passwords must contain 12–72 bytes. Every user can change their own local password. Password, role and account-status changes invalidate existing sessions immediately, including download requests and WebSocket reconnects. Open streams are rechecked periodically by the server. Logout revokes the current session durably across restarts.

At least one enabled **local administrator** must remain available, so an identity-provider outage does not lock out management. Disabling an account preserves its identity in audit history.

| Capability | Viewer | Operator | Administrator |
|---|---|---|---|
| Clusters, dashboard, nodes, workloads, storage | Read | Read | Full |
| Services, logs, health, audit, job history | Read | Read | Full |
| Backup metadata | Read | Read | Full |
| Cordon, drain, maintenance, reboot, service restart | — | Yes | Yes |
| Talos/Kubernetes updates and rolling reboot | — | Yes | Yes |
| Create backups | — | Yes | Yes |
| Download backups containing cluster credentials | — | — | Yes |
| Restore, shutdown and infrastructure deletion | — | — | Yes |
| MachineConfig changes and provisioning | — | — | Yes |
| Providers, credentials, settings and users | — | — | Yes |

Permissions apply on the server, including cluster-scoped routes. Hiding a button is not an authorization boundary. A job's immutable operation type also controls who may start, stop or review it. Roles currently apply to the entire fleet; per-cluster grants and custom roles are not implemented.

## Keycloak / OpenID Connect

Create an OpenID Connect **confidential client** in Keycloak:

1. Enable client authentication and the authorization-code / standard flow. Disable direct password grants.
2. Set the exact valid redirect URI to `https://talosdeck.example.com/api/auth/oidc/callback`. Do not use a wildcard.
3. Configure a groups mapper that includes a string-array `groups` claim in ID tokens. The default requested scopes are `openid profile email`.
4. Put users into explicitly mapped groups. Unmapped users receive Viewer access.

Configure TalosDeck with:

```dotenv
TALOSDECK_OIDC_ISSUER=https://keycloak.example.com/realms/infrastructure
TALOSDECK_OIDC_CLIENT_ID=talosdeck
TALOSDECK_OIDC_CLIENT_SECRET=replace-with-client-secret
TALOSDECK_OIDC_REDIRECT_URL=https://talosdeck.example.com/api/auth/oidc/callback
TALOSDECK_OIDC_NAME=Company SSO
TALOSDECK_OIDC_GROUPS_CLAIM=groups
TALOSDECK_OIDC_GROUP_ROLES={"/talosdeck-viewers":"viewer","/talosdeck-operators":"operator","/talosdeck-admins":"admin"}
```

Use the actual group values emitted by your mapper; Keycloak can include full paths. OIDC roles are recalculated from signed group claims at login. Administrators can disable OIDC users and revoke sessions locally, but their passwords and role mappings remain managed by the provider. Existing application sessions do not poll the identity provider for group changes: revoke sessions locally when immediate removal is required.

TalosDeck validates discovery, issuer, signature, audience, expiration and nonce. Login uses PKCE S256, single-use state bound to an HttpOnly cookie, and an exact configured redirect destination. Provider access/ID tokens are never returned to the frontend. The callback gives the browser a short-lived, single-use application sign-in code, which also requires its HttpOnly cookie. Local logout ends the TalosDeck session, not the provider's separate SSO session.

Issuer, discovery endpoints and redirect URLs must use HTTPS. `TALOSDECK_OIDC_ALLOW_HTTP=true` permits **loopback addresses only** for local tests. It does not permit plain HTTP to a production Keycloak host. An unavailable provider is reported at startup while local administrator login remains available; correct configuration and restart to re-enable SSO.

## Docker Compose outside managed clusters

Run the management service on a separate host or management cluster, so it remains available while managed clusters are being repaired.

Create a private `.env` alongside `compose.yaml`:

```dotenv
TALOSDECK_IMAGE=ghcr.io/etosheartem/talosdeck:your-published-tag
TALOSDECK_ADMIN_PASSWORD=replace-with-a-long-unique-password
```

```bash
chmod 600 .env
docker compose up -d
docker compose ps
```

The default address is `http://127.0.0.1:8080`. Import a cluster through the UI using its talosconfig and embedded kubeconfig. Set `TALOSDECK_BIND_ADDRESS` only when you intend to expose the service beyond localhost; terminate HTTPS with your reverse proxy for remote use and OIDC.

Compose creates separate `talosdeck-data` and `talosdeck-keys` volumes. A short initialization container sets ownership; the application runs as UID/GID 1000 with a read-only root filesystem. Temporary decrypted Talos credentials stay on a memory filesystem. The database and key volume must both survive container replacement. `docker compose down` preserves them; adding `--volumes` deletes them and prevents recovery without separate backups.

`TALOSDECK_JWT_SECRET` is optional. If omitted, a random signing key is saved inside encrypted state. Changing an explicitly configured signing key invalidates all sessions. Keep `.env` and the key volume private and back them up independently from the database.

## Helm in a management cluster

The chart is at `deploy/helm/talosdeck`. It deliberately runs **one replica with Recreate strategy**: SQLite and operation ownership are not an HA architecture. No cluster-wide Kubernetes role or service-account token is granted; managed clusters use the credentials explicitly imported into TalosDeck.

Create namespace and authentication Secret using a private environment file containing `TALOSDECK_ADMIN_PASSWORD` and, optionally, `TALOSDECK_JWT_SECRET` and `TALOSDECK_OIDC_CLIENT_SECRET`:

```bash
kubectl create namespace talosdeck
kubectl -n talosdeck create secret generic talosdeck-auth --from-env-file=auth.env
helm upgrade --install talosdeck ./deploy/helm/talosdeck \
  --namespace talosdeck \
  --set image.tag="$TALOSDECK_IMAGE_TAG"
```

Set `TALOSDECK_IMAGE_TAG` to a published immutable tag before running the command. Configure `ingress` values for an existing HTTPS ingress controller, or port-forward the generated Service. `helm template` shows its release-specific name.

The chart provisions separate data and encryption-key PVCs and retains them on uninstall. You can provide existing PVCs through `persistence.existingClaim` and `encryption.existingClaim`. For an externally managed key, set `encryption.existingSecret` to a Secret containing `master.key`; the Secret is mounted read-only with group-readable permissions. This file is TalosDeck's **JSON keyring**, not an arbitrary password or raw key. Preserve the existing keyring when moving an installation.

An optional `bootstrap.existingSecret` may contain both `talosconfig` and `kubeconfig`; they are mounted read-only for the one-time migration. Otherwise, start with an empty registry and import clusters through the UI.

OIDC chart settings live under `oidc`: `enabled`, `issuer`, `clientID`, `redirectURL`, `name`, `groupsClaim`, `groupRoles`. The client secret comes from `auth.existingSecret`; passwords and client secrets must not be committed in Helm values or GitOps manifests.

For encryption-key rotation, stop the application first and follow the database/key procedure in [operations](operations.md). A read-only Secret cannot be replaced by the running process: rotate using a writable private copy, update the external Secret, then restart with the matching database and updated keyring.

## API reference

The existing password-only login remains compatible by defaulting `username` to `admin`.

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/auth/login` | `{username,password}` → session token and user |
| GET | `/api/auth/me` | Current user, role and capabilities |
| POST | `/api/auth/logout` | Revoke this session |
| POST | `/api/auth/password` | `{currentPassword,password}` |
| GET / POST | `/api/auth/users` | List / create users (admin) |
| PATCH | `/api/auth/users/:id` | `{role?,disabled?}` (admin) |
| POST | `/api/auth/users/:id/password` | `{password}` reset (admin) |
| POST | `/api/auth/users/:id/revoke` | Revoke all user sessions (admin) |
| GET | `/api/auth/providers` | Public SSO availability, no secrets |
| GET | `/api/auth/oidc/login` | Start browser authorization flow |
| GET | `/api/auth/oidc/callback` | Provider callback; browser-bound state |
| POST | `/api/auth/oidc/exchange` | Browser-bound one-time sign-in code |
