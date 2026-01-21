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

## Roadmap

- [x] **Phase 1: Walking Skeleton** (Signaling, Matching, Basic P2P)
- [ ] **Phase 2: Auth & Matching Logic** (Phone auth, Redis queue)
- [ ] **Phase 3: Safety & Refinement** (Reporting, Screenshot capture)
- [ ] **Phase 4: Production** (Load testing, App Store submission)
