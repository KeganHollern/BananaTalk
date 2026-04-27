import 'dart:convert';

import 'package:flutter_webrtc/flutter_webrtc.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

import 'services/connect_timing.dart';
import 'services/logger_service.dart';

typedef OnLocalStream = void Function(MediaStream stream);
typedef OnRemoteStream = void Function(MediaStream stream);
typedef OnTimingReport = void Function(ConnectTiming timing);

class Signaling {
  final String serverUrl;
  final String token;
  WebSocketChannel? _channel;
  RTCPeerConnection? _peerConnection;
  MediaStream? _localStream;

  OnLocalStream? onLocalStream;
  OnRemoteStream? onRemoteStream;
  void Function()? onCallEnded;
  void Function(dynamic error)? onConnectionError;

  /// Fired once per match when the timing report is sent to the backend.
  /// The renderer can also drive [reportFirstFrame] later if it detects an
  /// actual painted frame; that just enriches the same in-memory report.
  OnTimingReport? onTimingReport;

  String? _selfId;
  String? _remoteId;

  ConnectTiming? _timing;

  /// Remote ICE candidates that arrive before the local peer connection has
  /// applied the corresponding remote description. Adding a candidate before
  /// setRemoteDescription throws on flutter_webrtc, so we buffer and flush
  /// once the description is in place.
  final List<RTCIceCandidate> _pendingRemoteCandidates = [];
  bool _remoteDescriptionSet = false;

  Signaling(this.serverUrl, this.token);

  String? get remoteId => _remoteId;
  ConnectTiming? get timing => _timing;

  Future<void> connect() async {
    final urlWithToken = '$serverUrl?token=$token';
    _channel = WebSocketChannel.connect(Uri.parse(urlWithToken));

    _channel!.stream.listen(
      (message) {
        _handleMessage(jsonDecode(message));
      },
      onError: (error) {
        LoggerService().logError(
            'Signaling', 'WebSocket error', error, StackTrace.current);
        onConnectionError?.call(error);
      },
      onDone: () {
        LoggerService().logInfo('Signaling', 'WebSocket closed');
        onCallEnded?.call();
      },
    );
  }

  void _handleMessage(Map<String, dynamic> msg) async {
    final type = msg['type'];
    final payload = msg['payload'];

    switch (type) {
      case 'init':
        _selfId = payload;
        LoggerService().logInfo('Signaling', 'My ID: $_selfId');
        break;
      case 'match':
        _remoteId = payload;
        _timing?.matchAssignedAt = DateTime.now();
        LoggerService().logInfo('Signaling',
            'Matched with: $_remoteId (queue_wait=${_timing?.matchAssignedAt?.difference(_timing!.queueJoinedAt).inMilliseconds}ms)');
        // Glare prevention: lower-ID peer is the offerer.
        if (_selfId!.compareTo(_remoteId!) < 0) {
          _timing?.role = PeerRole.offerer;
          LoggerService().logInfo('Signaling', 'I am the offerer');
          _createOffer();
        } else {
          _timing?.role = PeerRole.answerer;
          LoggerService()
              .logInfo('Signaling', 'I am the answerer, waiting for offer...');
        }
        break;
      case 'offer':
        _handleOffer(msg['from'], payload);
        break;
      case 'answer':
        _handleAnswer(payload);
        break;
      case 'ice_candidate':
        _handleIceCandidate(payload);
        break;
      case 'bye':
        if (_inCall()) {
          LoggerService().logInfo('Signaling', 'Peer disconnected');
          onCallEnded?.call();
        }
        break;
    }
  }

  bool _inCall() {
    return _peerConnection != null;
  }

  Future<void> openUserMedia() async {
    if (_localStream != null) return;
    final Map<String, dynamic> mediaConstraints = {
      'audio': true,
      'video': {
        'facingMode': 'user',
      }
    };

    _localStream = await navigator.mediaDevices.getUserMedia(mediaConstraints);
    onLocalStream?.call(_localStream!);
  }

  /// Pre-warms a single match attempt: starts a fresh ConnectTiming, builds
  /// the RTCPeerConnection, attaches local tracks, and lets ICE candidate
  /// pre-gathering begin (via iceCandidatePoolSize) — all before a peer is
  /// even assigned. Call this immediately on entering the matching queue.
  Future<void> prewarm() async {
    await openUserMedia();
    _timing = ConnectTiming();
    _remoteDescriptionSet = false;
    _pendingRemoteCandidates.clear();
    _peerConnection = await _createPeerConnection();
  }

  Future<void> _createOffer() async {
    final pc = _peerConnection;
    if (pc == null) {
      LoggerService().logError('Signaling',
          'createOffer called without a pre-warmed peer connection',
          null, StackTrace.current);
      return;
    }
    RTCSessionDescription offer = await pc.createOffer();
    _timing?.offerCreatedAt = DateTime.now();
    await pc.setLocalDescription(offer);
    _send('offer', offer.toMap(), to: _remoteId);
    _timing?.offerSentAt = DateTime.now();
  }

  Future<void> _handleOffer(String from, dynamic payload) async {
    _remoteId = from;
    _timing?.remoteOfferReceivedAt = DateTime.now();
    final pc = _peerConnection ?? await _createPeerConnection();
    _peerConnection = pc;

    await pc.setRemoteDescription(
      RTCSessionDescription(payload['sdp'], payload['type']),
    );
    _remoteDescriptionSet = true;
    await _flushPendingRemoteCandidates();

    RTCSessionDescription answer = await pc.createAnswer();
    await pc.setLocalDescription(answer);

    _send('answer', answer.toMap(), to: _remoteId);
    _timing?.answerSentAt = DateTime.now();
  }

  Future<void> _handleAnswer(dynamic payload) async {
    final pc = _peerConnection;
    if (pc == null) return;
    await pc.setRemoteDescription(
      RTCSessionDescription(payload['sdp'], payload['type']),
    );
    _remoteDescriptionSet = true;
    _timing?.answerReceivedAt = DateTime.now();
    await _flushPendingRemoteCandidates();
  }

  Future<void> _handleIceCandidate(dynamic payload) async {
    final pc = _peerConnection;
    if (pc == null) return;
    final candidate = RTCIceCandidate(
      payload['candidate'],
      payload['sdpMid'],
      payload['sdpMLineIndex'],
    );
    if (!_remoteDescriptionSet) {
      _pendingRemoteCandidates.add(candidate);
      return;
    }
    await pc.addCandidate(candidate);
  }

  Future<void> _flushPendingRemoteCandidates() async {
    final pc = _peerConnection;
    if (pc == null || _pendingRemoteCandidates.isEmpty) return;
    final pending = List<RTCIceCandidate>.from(_pendingRemoteCandidates);
    _pendingRemoteCandidates.clear();
    for (final c in pending) {
      try {
        await pc.addCandidate(c);
      } catch (e, s) {
        LoggerService()
            .logError('Signaling', 'Failed to add buffered ICE candidate', e, s);
      }
    }
  }

  Future<RTCPeerConnection> _createPeerConnection() async {
    // iceCandidatePoolSize lets the engine pre-gather ICE candidates before
    // setLocalDescription is called. Combined with prewarm() it shaves the
    // gathering phase off the post-match critical path.
    Map<String, dynamic> configuration = {
      'iceServers': [
        {'url': 'stun:stun.l.google.com:19302'},
      ],
      'iceCandidatePoolSize': 2,
    };

    RTCPeerConnection pc = await createPeerConnection(configuration);

    _localStream?.getTracks().forEach((track) {
      pc.addTrack(track, _localStream!);
    });

    pc.onIceCandidate = (RTCIceCandidate candidate) {
      _send(
          'ice_candidate',
          {
            'candidate': candidate.candidate,
            'sdpMid': candidate.sdpMid,
            'sdpMLineIndex': candidate.sdpMLineIndex,
          },
          to: _remoteId);
    };

    pc.onIceGatheringState = (RTCIceGatheringState state) {
      LoggerService()
          .logInfo('Signaling', 'ICE gathering state: $state');
      if (state == RTCIceGatheringState.RTCIceGatheringStateComplete &&
          _timing != null &&
          _timing!.matchAssignedAt != null &&
          _timing!.iceGatheringCompleteAt == null) {
        _timing!.iceGatheringCompleteAt = DateTime.now();
      }
    };

    pc.onTrack = (RTCTrackEvent event) {
      if (event.streams.isNotEmpty) {
        if (_timing != null && _timing!.firstRemoteTrackAt == null) {
          _timing!.firstRemoteTrackAt = DateTime.now();
        }
        onRemoteStream?.call(event.streams[0]);
        _maybeReportTiming();
      }
    };

    pc.onConnectionState = (RTCPeerConnectionState state) {
      LoggerService().logInfo('Signaling', 'Connection state change: $state');
      if (state == RTCPeerConnectionState.RTCPeerConnectionStateDisconnected ||
          state == RTCPeerConnectionState.RTCPeerConnectionStateFailed) {
        onCallEnded?.call();
      }
    };

    return pc;
  }

  /// Called by the UI once the remote video renderer has painted its first
  /// frame. Updates the in-memory timing and re-emits the report so the
  /// `first_frame_ms` field is populated end-to-end.
  void reportFirstFrame() {
    final t = _timing;
    if (t == null || t.firstRemoteFrameAt != null) return;
    t.firstRemoteFrameAt = DateTime.now();
    _maybeReportTiming();
  }

  void _maybeReportTiming() {
    final t = _timing;
    if (t == null || t.reported) return;
    if (t.firstRemoteTrackAt == null) return;
    final report = t.toReport();
    _send('connect_metrics', report);
    t.markReported();
    onTimingReport?.call(t);
  }

  void _send(String type, dynamic payload, {String? to}) {
    _channel?.sink.add(jsonEncode({
      'type': type,
      'payload': payload,
      'to': to,
    }));
  }

  void sendBye() {
    _send('bye', {}, to: _remoteId);
  }

  /// Tears down the current peer connection and re-enters the matching queue,
  /// keeping the local stream and WebSocket connection alive. The new PC is
  /// pre-warmed in the same call so the next match starts with media + ICE
  /// already primed.
  Future<void> findNextMatch() async {
    sendBye();
    final pc = _peerConnection;
    _peerConnection = null;
    _remoteId = null;
    _remoteDescriptionSet = false;
    _pendingRemoteCandidates.clear();
    pc?.dispose();
    _send('next_match', {});
    _timing = ConnectTiming();
    _peerConnection = await _createPeerConnection();
  }

  void dispose() {
    _localStream?.dispose();
    _peerConnection?.dispose();
    _channel?.sink.close();
    _localStream = null;
    _peerConnection = null;
    _channel = null;
  }
}
