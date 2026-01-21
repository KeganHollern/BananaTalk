Here is a comprehensive development plan for the random video chat application. This roadmap is tailored for a technical team using **Flutter** for the client and **Go (Golang)** for the high-concurrency backend.

---

## 1. High-Level Architecture

The system relies on **WebRTC** for peer-to-peer video streaming, with the Go backend acting as the Signaling Server and API handler.

### Core Components

- **Client (Flutter):** Handles camera, UI gestures (swipe), and WebRTC streams.
- **Backend (Go):**
- **REST/gRPC API:** For authentication and reporting.
- **WebSocket Signaling Server:** For real-time matching and exchanging WebRTC connection data (SDP/ICE candidates).
- **Worker Pool:** For processing reports and screenshots asynchronously.

- **Infrastructure:**
- **Redis:** For the "Waiting Queue" (low latency matching) and session management.
- **PostgreSQL:** For persistent user data and report logs.
- **TURN/STUN Server:** (e.g., Coturn) Essential for traversing NATs/Firewalls so peers can connect.

---

## 2. UX/UI Design Specifications

The interface should be minimalist, focusing entirely on the video feed.

### Core Screens & Flow

1. **Onboarding (The Gate):**

- Action: "Sign in with Google" or "Sign in with Apple" buttons.
- Verification: OS-level OAuth flow.
- Permissions: Request Camera/Mic access immediately after auth.

1. **The "Feed" (Main View):**

- **Full-screen Video:** The remote user fills the screen.
- **PIP (Picture-in-Picture):** Local user face shown in a small, draggable rectangle.
- **Overlay Controls:** Minimalist. A small "Report" shield icon in the corner.

1. **The Interaction (The Swipe):**

- **Gesture:** Vertical Swipe Up.
- **Animation:** The current video slides up and off-screen; a "Connecting..." blurred state or spinner appears briefly; the new video slides in from the bottom.

### Design Mockup Data

| UI Element        | Action                                       | Feedback                                                        |
| ----------------- | -------------------------------------------- | --------------------------------------------------------------- |
| **Swipe Up**      | Disconnect current peer -> Join Match Queue. | Haptic feedback (light vibration). Screen transition animation. |
| **Report Button** | Opens modal: "Reason for reporting?".        | Pauses video stream. Captures last frame of remote video.       |
| **Report Submit** | Uploads image + metadata.                    | Toast message: "User reported. Finding new match..."            |

---

## 3. Backend Engineering (Go)

Go is excellent here for its concurrency primitives (goroutines) which handle thousands of WebSocket connections cheaply.

### A. API Layer (Gin or Echo Framework)

- `POST /auth/login`: Accepts OAuth provider token (Google/Apple).
- `POST /auth/refresh`: Refreshes the JWT.
- `POST /report`: Accepts multipart form data (Screenshot Image + UserID + Reason).

### B. The Matching Engine (WebSockets + Redis)

This is the heart of the app. It must be event-driven.

**Data Structures:**

- **The Pool:** A Redis Set or List containing `user_id`s currently waiting for a match.
- **The Session:** A Redis Key mapping `user_id` -> `current_peer_id`.

**Matching Logic (Go Routine):**

1. **Connect:** User connects via WebSocket (`ws://api/signal`).
2. **Queue:** User sends "Looking for match". Server pushes user to Redis Queue.
3. **Match:**

- A Loop checks the queue length.
- If `len > 1`, pop two users.
- Create a `MatchID`.
- Send `match_found` event to both users with the `MatchID`.
- _Geolocation Logic:_ During the "pop" phase, match users based on Country derived from IP address. For MVP, rely on simple GeoIP lookups.

### C. Signaling (WebRTC Exchange)

Once matched, the Go server purely relays messages between User A and User B:

- **Event:** `offer` (Payload: SDP) Forward to Peer.
- **Event:** `answer` (Payload: SDP) Forward to Peer.
- **Event:** `ice_candidate` Forward to Peer.

---

## 4. Frontend Engineering (Flutter)

### A. Dependencies

- `flutter_webrtc`: Standard plugin for WebRTC in Flutter.
- `socket_io_client` or `web_socket_channel`: For signaling.
- `camera`: For local previews if not using the WebRTC renderer.
- `dio`: For HTTP API calls.

### B. State Management (Bloc or Riverpod)

You need a robust state machine for the call status:

- `CallState.idle`: Not connected.
- `CallState.matching`: Showing loading spinner/animation.
- `CallState.connected`: Displaying remote stream.
- `CallState.reporting`: Paused, uploading data.

### C. Implementing the "Swipe to Next"

This requires careful resource management to prevent memory leaks.

1. **On Swipe Up:**

- Trigger `_signaling.disconnect()`.
- Dispose the `RTCVideoRenderer` of the current peer.
- Keep the **Local Stream** alive (do not re-initialize camera).
- Send `socket.emit('next_match')`.

1. **Transition:**

- Use a `PageView` with vertical scrolling physics.
- The "Next" page is always the loading state until the `match_found` event fires.

### D. The Screenshot Feature

Taking a screenshot of a `RTCVideoRenderer` (Texture) is tricky.

- **Approach:** Use `RepaintBoundary` wrapping the Video Renderer.
- **Execution:**

```dart
RenderRepaintBoundary boundary = currentContext.findRenderObject();
ui.Image image = await boundary.toImage();
ByteData byteData = await image.toByteData(format: ui.ImageByteFormat.png);
// Send byteData to Go backend

```

- _Note:_ If `RepaintBoundary` captures a black screen (common with Platform Views), use the `flutter_webrtc` native method: `mediaStreamTrack.captureFrame()`.

---

## 5. Trust & Safety Implementation

Since this is an anonymous video chat, safety is the highest risk factor.

### Automated Bucketing

- **Extraction:** On sign-up/connect, capture the user's IP Address.
- **Logic:** Use a GeoIP database to identify the user's Country. Use this for matching preference and for safety reporting buckets.
- **Future (Phase 3+):** Implement VPN/Proxy detection to flag suspicious users trying to spoof location.

### Reporting Pipeline

1. **User Action:** User taps report.
2. **Client:**

- Captures frame.
- Submits to `POST /report`.
- **Crucial:** Blocks the reported user locally (store `blocked_ids` in shared prefs or backend) so they don't rematch immediately.

1. **Backend:**

- Stores the image in Object Storage (S3/GCS).
- Flags the reported user in the database (`reports_received_count++`).
- **Ban Logic:** If `reports_received_count > threshold` within `time_window`, automatically suspend the account.

---

## 6. Development Roadmap

### Phase 1: The Walking Skeleton (Weeks 1-3)

- **Goal:** Two phones can connect via WebSocket and stream video.
- **Tasks:**
- Setup Go signaling server (echo messages).
- Flutter app with hardcoded "Join" button.
- Basic WebRTC connection (P2P).
- Setup Coturn (TURN server) on a cheap VPS.

### Phase 2: Auth & Matching Logic (Weeks 4-5)

- **Goal:** Phone auth and random pairing.
- **Tasks:**
- Integrate Google and Apple Sign-In.
- Implement Redis queue in Go.
- Implement "Swipe Up" gesture in Flutter to re-trigger matching.

### Phase 3: Safety & Refinement (Weeks 6-7)

- **Goal:** Reporting and Polish.
- **Tasks:**
- Implement Screenshot capture.
- Build the Admin Dashboard (to view reported screenshots).
- Optimize connection speed (aim for <2s connection time).

### Phase 4: Production (Week 8)

- **Goal:** Launch.
- **Tasks:**
- Load testing (Simulate 1k concurrent websockets).
- App Store / Play Store compliance (Note: Apple requires strict moderation for UGC apps; you **must** show you have blocking/reporting features).

### Would you like me to generate the Go code for the WebSocket signaling handler or the Flutter widget code for the swipeable video container?
