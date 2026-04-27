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

## Roadmap

- [x] **Phase 1: Walking Skeleton** (Signaling, Matching, Basic P2P)
- [x] **Phase 2: Auth & Matching Logic** (Social Auth, Redis queue)
- [ ] **Phase 3: Safety & Refinement** (Reporting, Screenshot capture)
- [ ] **Phase 4: Production** (Load testing, App Store submission)
