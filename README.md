# BananaTalk 🍌

A random video chat application built with Flutter and Go.

## Project Structure

- `backend/`: Go signaling server using WebSockets.
- `frontend/`: Flutter mobile application.

## Getting Started

### Phase 1: The Walking Skeleton (Current Status)

Two clients can connect to the signaling server, get matched, and establish a P2P WebRTC connection.

#### Running the Backend

```bash
cd backend
go run main.go
```

The server will start on `localhost:8080`.

#### Running the Frontend

1. Ensure you have Flutter installed.
2. Navigate to the frontend directory:

   ```bash
   cd frontend
   flutter pub get
   flutter run
   ```

_Note: For Phase 1, the frontend is configured to connect to `localhost:8080`. For testing on real devices, update the IP address in `lib/main.dart`._

### Infrastructure

#### STUN/TURN Server

For Phase 1, we use Google's public STUN server (`stun:stun.l.google.com:19302`).
For production (traversing symmetric NATs), you should set up a Coturn server.

**Coturn Setup (Example):**

```bash
sudo apt-get install coturn

# Edit /etc/turnserver.conf with your realm and credentials
sudo systemctl start coturn
```

### Backend Environment Variables

| Variable | Default | Description |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | Redis server address (`host:port`) |
| `REDIS_PASSWORD` | _(empty)_ | Redis `requirepass` password |
| `REDIS_DB` | `0` | Redis logical database index |
| `DB_DSN` | _(required)_ | PostgreSQL connection string (`postgres://user:pass@host:5432/db`) |
| `STORAGE_PROVIDER` | _(required)_ | `s3` or `gcs` |
| `STORAGE_BUCKET` | _(required)_ | Object storage bucket name for report screenshots |
| `STORAGE_S3_ENDPOINT` | _(empty)_ | Override S3 endpoint (for MinIO etc.); enables path-style requests |
| `STORAGE_PUBLIC_URL_BASE` | _(empty)_ | If set, screenshot URLs use this prefix and signing is skipped (assumes a public bucket / CDN) |
| `ADMIN_USERNAME` | _(empty)_ | If set together with `ADMIN_PASSWORD`, mounts the moderation dashboard at `/admin/` |
| `ADMIN_PASSWORD` | _(empty)_ | Basic-auth password for the moderation dashboard |

## Admin Dashboard

A minimal moderation dashboard is served by the Go backend itself (no extra
service to deploy). It is **disabled** unless both `ADMIN_USERNAME` and
`ADMIN_PASSWORD` are set in the environment.

### Local

```bash
ADMIN_USERNAME=admin ADMIN_PASSWORD=hunter2 \
DB_DSN=postgres://... STORAGE_PROVIDER=s3 STORAGE_BUCKET=... \
go run .
```

Open `http://localhost:8080/admin/` and authenticate with the credentials
above. The browser caches Basic auth for the session; close the tab to log
out.

### Endpoints

All endpoints require HTTP Basic auth. Static dashboard files are served at
`/admin/*`; the JSON API is rooted at `/admin/api/`:

| Method | Path | Description |
|---|---|---|
| `GET` | `/admin/api/reports?reason=&page=1&limit=20` | Paginated report list (newest first), optional `reason` substring filter |
| `GET` | `/admin/api/reports/{id}` | Single report detail with a 15-minute signed screenshot URL |
| `POST` | `/admin/api/users/{id}/ban` | Manually ban the user (also drops their websocket if connected) |
| `POST` | `/admin/api/users/{id}/unban` | Lift a ban |

### Production

The dashboard is served on the same `/admin/` path as the rest of the
backend. **Restrict access at the ingress** in addition to the in-app Basic
auth — for example with the nginx-ingress `nginx.ingress.kubernetes.io/whitelist-source-range`
annotation, a separate hostname behind a VPN, or a dedicated ingress that
only routes `/admin/`. Set `ADMIN_USERNAME` and `ADMIN_PASSWORD` via a
Kubernetes Secret, not in the deployment YAML directly.

For S3-backed deployments, the running service needs `s3:GetObject` to
generate presigned URLs. For GCS, the service account needs
`iam.serviceAccounts.signBlob` (or use a JSON key) for signed URLs. If
`STORAGE_PUBLIC_URL_BASE` is set, signing is skipped — make sure the bucket
or CDN behind that prefix is appropriately scoped, since admin screenshot
URLs will be returned verbatim.

In Kubernetes these are injected from the `redis-secret` Secret (see `k8s/base/redis-secret.yaml`). Replace the placeholder values before applying:

```bash
# Edit placeholder values first
kubectl apply -k k8s/base/
```

**Secrets with placeholder values that must be changed before deploying:**

| Secret | Key | Used by |
|---|---|---|
| `redis-secret` | `redis-password` | Backend (`REDIS_PASSWORD`), Redis (`--requirepass`) |
| `postgres-secret` | `postgres-password`, `postgres-user`, `postgres-db` | PostgreSQL StatefulSet |

## Kubernetes

### Health probes

The backend exposes two unauthenticated probe endpoints:

| Endpoint | Purpose | Behavior |
|---|---|---|
| `GET /healthz` | Liveness | Always returns `200 ok` if the HTTP server is running. No dependency checks — a transient Redis/Postgres blip must not restart pods that are still serving live WebRTC signaling. |
| `GET /readyz` | Readiness | Pings Redis and Postgres with a short (1.5s) per-check deadline. Returns `503` if either is unreachable, which removes the pod from the Service endpoints (no new traffic) without killing in-flight sessions. |

Probe tuning in `k8s/base/deployment.yaml` is sized for a long-lived WebSocket
server: liveness runs every 10s with a 3-failure threshold (~30s grace before
restart), readiness runs every 5s so traffic shifts off a degraded pod
quickly.

### Horizontal Pod Autoscaler

`k8s/base/hpa.yaml` autoscales the `bananatalk-backend` Deployment on CPU
utilization, defaulting to `min=2`, `max=10`, target `70%`. The scale-up
behavior is aggressive (up to +100% or +2 pods every 30s) so the fleet can
respond to the Phase 4 load test, while scale-down uses a 5-minute
stabilization window to avoid flapping when WS connection counts are bursty.

To override min/max for a specific environment, patch the HPA via Kustomize.
For example, in an overlay:

```yaml
# k8s/overlays/loadtest/kustomization.yaml
resources:
  - ../../base
patches:
  - target:
      kind: HorizontalPodAutoscaler
      name: bananatalk-backend
    patch: |
      - op: replace
        path: /spec/minReplicas
        value: 4
      - op: replace
        path: /spec/maxReplicas
        value: 25
```

Or imperatively (does not survive a re-apply of the manifest):

```bash
kubectl autoscale deployment bananatalk-backend --min=4 --max=25 --cpu-percent=70
# or
kubectl patch hpa bananatalk-backend --patch '{"spec":{"minReplicas":4,"maxReplicas":25}}'
```

The `bananatalk-backend` PodDisruptionBudget uses `maxUnavailable: 1` so
voluntary disruptions (node drains, cluster upgrades) stay serial as the HPA
scales replicas up — preventing a single drain from dropping a large block of
WebSocket sessions at peak.

### Graceful shutdown

On `SIGTERM` or `SIGINT` (rolling updates, HPA scale-down, node drains) the
backend runs a five-phase drain instead of dropping connections abruptly:

1. Flip an internal `shuttingDown` flag — `/readyz` starts returning `503`
   so the kubelet pulls the pod from the Service endpoints, and any new
   `/ws` upgrade is rejected with `503 shutting_down`.
2. Sleep ~3s so the readiness probe has a tick to propagate.
3. Broadcast a `{"type": "server_shutdown"}` JSON message to every connected
   client. The Flutter client treats this as a transient event: it tears
   down the peer connection, transitions to the "Reconnecting…" state, and
   re-establishes the WebSocket with exponential backoff against a healthy
   replica.
4. Send a clean WS `CloseGoingAway` frame to each client and close the
   socket — the existing per-connection cleanup (matchmaker queue/session
   eviction) runs from its `defer`.
5. `http.Server.Shutdown` with a 25s deadline drains any non-WS handlers
   (`/admin`, `/report`, `/metrics`).

The Pod's `terminationGracePeriodSeconds` is set to **35s** in
`k8s/base/deployment.yaml` to comfortably cover the 25s shutdown deadline
plus the readiness drain pause before SIGKILL fires.

## Roadmap

- [x] **Phase 1: Walking Skeleton** (Signaling, Matching, Basic P2P)
- [x] **Phase 2: Auth & Matching Logic** (Social Auth, Redis queue)
- [ ] **Phase 3: Safety & Refinement** (Reporting, Screenshot capture)
- [ ] **Phase 4: Production** (Load testing, App Store submission)
