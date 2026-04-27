# Phase 3 — Connection Speed Optimization

## Goal

Median time from match notification to first remote video frame rendered: **< 2s** on Wi-Fi.
p95: **< 4s** on Wi-Fi.

The clock starts when the client receives the `match` WebSocket message and stops when the remote `RTCVideoRenderer` paints its first frame.

## What got instrumented

### Backend (Prometheus)

Defined in `backend/metrics.go`:

| Metric | Type | Source | Notes |
|---|---|---|---|
| `bananatalk_match_latency_seconds` | Histogram | server-only | Time from queue enqueue to match assignment. Recorded in `MatchMaker.observeMatchLatency` using a Redis hash (`matchmaker:enqueued_at`) of unix-nanos timestamps. Cleared on match or on `Remove`. |
| `bananatalk_connect_time_seconds{phase, role}` | HistogramVec | client-reported | Per-phase milestone time, measured from match assignment by the client and reported via the `connect_metrics` WS message. |
| `bananatalk_queue_wait_seconds` | Histogram | client-reported | Time the client spent waiting between joining the queue and receiving the match message (client-clock). |

`phase` label values:
- `total` — match → first remote frame painted
- `first_track` — match → `onTrack` fired
- `ice_gathering` — match → `iceGatheringState == complete`
- `sdp_offer` — match → local offer sent (offerer only)
- `sdp_answer` — match → remote answer applied (offerer only)

`role` label is `offerer` or `answerer`.

### Frontend (Flutter)

`frontend/lib/services/connect_timing.dart` defines `ConnectTiming`. `Signaling` records milestones into it as the handshake progresses:
- `queueJoinedAt` — set in `prewarm()`
- `matchAssignedAt` — set on `match` message receipt (this is t0 for everything below)
- `offerCreatedAt`, `offerSentAt` (offerer)
- `remoteOfferReceivedAt`, `answerSentAt` (answerer)
- `answerReceivedAt` (offerer)
- `iceGatheringCompleteAt` — set on `iceGatheringState == complete`
- `firstRemoteTrackAt` — set on `onTrack`
- `firstRemoteFrameAt` — set when `RTCVideoRenderer.renderVideo` flips true (the renderer's first painted frame)

`_maybeReportTiming()` fires once per match — as soon as `firstRemoteTrackAt` is set — by sending a `connect_metrics` WS message. `firstRemoteFrameAt` may be filled in shortly after; it's logged locally but does not re-emit (we report once on the cheaper, more deterministic `onTrack` event and rely on the `first_frame_ms` field from the renderer for the painted-frame number, which the UI feeds back through `Signaling.reportFirstFrame()`).

> Trade-off: we report on `onTrack` rather than waiting for `firstRemoteFrame` so the metric is captured even if rendering stalls. The `first_frame_ms` field in the same payload supplies the rendered-frame number when available; clients that disconnect before paint will still produce a `first_track` sample.

Debug logs (visible in stdout / FlutterLogs on Android):
- `Signaling: Matched with: <id> (queue_wait=Xms)`
- `Signaling: ICE gathering state: <state>`
- `Signaling: Connection state change: <state>`
- `ConnectTiming: reported: <full report map>` — emitted from `markReported()` so a developer can read all milestones in one log line.

## Optimizations applied

1. **Pre-warm `RTCPeerConnection` + local media on queue entry** — `Signaling.prewarm()` is now called before `connect()` in `_join()` (and inside `findNextMatch()` for swipe-to-next). `getUserMedia` (the slowest step on a real device, often 200–1000 ms while the camera spins up), `createPeerConnection`, and `addTrack` all happen during the queue-wait window instead of after match assignment.
2. **Pre-gather ICE candidates** — `iceCandidatePoolSize: 2` is set in the peer-connection config so the engine starts gathering host/srflx candidates as soon as the PC exists, before `setLocalDescription` is called. With pre-warm this means the first batch of candidates is typically ready by the time the offer is created.
3. **Trickle ICE end-to-end** — verified candidates are sent immediately as `onIceCandidate` fires (no batching). Added a buffer (`_pendingRemoteCandidates`) for remote candidates that arrive before `setRemoteDescription` resolves; flushed in both `_handleOffer` (answerer) and `_handleAnswer` (offerer). Without the buffer, early candidates were silently dropped on flutter_webrtc, forcing the connection to wait for re-gathered candidates.
4. **Backend signal relay** — confirmed `handleMessage` in `backend/main.go` does no buffering: each inbound message is looked up in the in-memory `clients` map and forwarded with a single `WriteJSON` (10 s write deadline, no queueing). The only synchronization is a per-client `sync.Mutex` around the WebSocket write to serialize concurrent producers.

## Methodology — measuring

Run the backend and a real Android/iOS device pair on the same Wi-Fi. After at least 30 paired calls:

```promql
histogram_quantile(0.50, sum by (le) (rate(bananatalk_connect_time_seconds_bucket{phase="total"}[10m])))
histogram_quantile(0.95, sum by (le) (rate(bananatalk_connect_time_seconds_bucket{phase="total"}[10m])))
```

Per-phase breakdown (where the time is going):

```promql
histogram_quantile(0.50, sum by (phase, le) (rate(bananatalk_connect_time_seconds_bucket[10m])))
```

Server-side queue health (independent of the client):

```promql
histogram_quantile(0.50, rate(bananatalk_match_latency_seconds_bucket[10m]))
```

## Baseline (pre-Phase-3)

> **TBD** — fill in once a paired-device run is captured against the pre-change build (commit `4fe51cd`).
>
> Run instructions: deploy the previous build, run 30+ matches, capture the `bananatalk_connect_time_seconds` and `bananatalk_match_latency_seconds` histograms, paste the p50/p95 here per phase. Do this on Wi-Fi with the device pair.

| Phase | p50 (s) | p95 (s) |
|---|---|---|
| total |  |  |
| first_track |  |  |
| ice_gathering |  |  |
| sdp_offer |  |  |
| sdp_answer |  |  |

## Post-change (with pre-warm + trickle + ICE pool)

> **TBD** — fill in once the same paired-device run is captured against this branch.

| Phase | p50 (s) | p95 (s) | Δ p50 | Δ p95 |
|---|---|---|---|---|
| total |  |  |  |  |
| first_track |  |  |  |  |
| ice_gathering |  |  |  |  |
| sdp_offer |  |  |  |  |
| sdp_answer |  |  |  |  |

Acceptance:
- [ ] median total < 2.0 s on Wi-Fi
- [ ] p95 total < 4.0 s on Wi-Fi

## TURN (ticket c877f)

Currently only `stun:stun.l.google.com:19302` is configured. When TURN is added per c877f the same instrumentation captures TURN-relayed connections automatically — `connect_metrics` reports `ice_gathering_complete_ms` regardless of candidate type, and the histogram buckets extend to 20 s for the slower TURN path.

To break TURN out of the bucket once it ships, add a second label (e.g. `relay`) to `bananatalk_connect_time_seconds` populated from the chosen ICE candidate's `type` (host / srflx / relay). Recommend doing that as part of the c877f rollout, not now — premature without TURN traffic to validate against.
