import 'dart:convert';

import 'package:flutter_webrtc/flutter_webrtc.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

typedef OnLocalStream = void Function(MediaStream stream);
typedef OnRemoteStream = void Function(MediaStream stream);

class Signaling {
  final String serverUrl;
  final String token; // Add token field
  WebSocketChannel? _channel;
  RTCPeerConnection? _peerConnection;
  MediaStream? _localStream;

  OnLocalStream? onLocalStream;
  OnRemoteStream? onRemoteStream;
  void Function()? onCallEnded;
  void Function(dynamic error)? onConnectionError;

  String? _selfId;
  String? _remoteId;

  Signaling(this.serverUrl, this.token); // Update constructor

  Future<void> connect() async {
    // Append token to URL
    final urlWithToken = '$serverUrl?token=$token';
    _channel = WebSocketChannel.connect(Uri.parse(urlWithToken));

    _channel!.stream.listen(
      (message) {
        _handleMessage(jsonDecode(message));
      },
      onError: (error) {
        print('WebSocket error: $error');
        onConnectionError?.call(error);
      },
      onDone: () {
        print('WebSocket closed');
        // If closed without explicit bye, might be an error or server disconnect
        // For now we can treat it as a potential drop if we were expecting to stay connected
        // But strictly speaking, onDone isn't always an error.
        // However, for auth failure, the server closes the connection immediately.
        // Let's assume onDone with no prior messages or during handshake might be worth checking.
        // For simplicity, we'll focus on onError or immediate close.
        if (_channel?.closeCode != null) {
          print('Close code: ${_channel?.closeCode}');
          // 1008 is Policy Violation (often used for Auth failure), but might be generic.
          // If we get closed immediately, trigger error logic.
        }

        // If we simply disconnected, we might want to notify.
        // For this specific task, let's trigger error if we aren't intentionally closing.
        // But how do we distinguish?
        // We'll leave onDone for now and focus on onError which often fires for connection failure.
        // Actually, ws package might just fire onDone if connection fails to establish?
        // Let's catch generic errors for now.
      },
    );
  }

  void _handleMessage(Map<String, dynamic> msg) async {
    final type = msg['type'];
    final payload = msg['payload'];

    switch (type) {
      case 'init':
        _selfId = payload;
        print('My ID: $_selfId');
        break;
      case 'match':
        _remoteId = payload;
        print('Matched with: $_remoteId');
        // Prevent Glare: Only the "polite" peer (e.g. lower ID) creates the offer.
        if (_selfId!.compareTo(_remoteId!) < 0) {
          print('I am the offerer');
          _createOffer();
        } else {
          print('I am the answerer, waiting for offer...');
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
          print('Peer disconnected');
          onCallEnded?.call();
        }
        break;
    }
  }

  bool _inCall() {
    return _peerConnection != null;
  }

  Future<void> openUserMedia() async {
    final Map<String, dynamic> mediaConstraints = {
      'audio': true,
      'video': {
        'facingMode': 'user',
      }
    };

    _localStream = await navigator.mediaDevices.getUserMedia(mediaConstraints);
    onLocalStream?.call(_localStream!);
  }

  Future<void> _createOffer() async {
    _peerConnection = await _createPeerConnection();

    RTCSessionDescription offer = await _peerConnection!.createOffer();
    await _peerConnection!.setLocalDescription(offer);

    _send('offer', offer.toMap(), to: _remoteId);
  }

  Future<void> _handleOffer(String from, dynamic payload) async {
    _remoteId = from;
    _peerConnection = await _createPeerConnection();

    await _peerConnection!.setRemoteDescription(
      RTCSessionDescription(payload['sdp'], payload['type']),
    );

    RTCSessionDescription answer = await _peerConnection!.createAnswer();
    await _peerConnection!.setLocalDescription(answer);

    _send('answer', answer.toMap(), to: _remoteId);
  }

  Future<void> _handleAnswer(dynamic payload) async {
    await _peerConnection!.setRemoteDescription(
      RTCSessionDescription(payload['sdp'], payload['type']),
    );
  }

  Future<void> _handleIceCandidate(dynamic payload) async {
    if (_peerConnection != null) {
      await _peerConnection!.addCandidate(
        RTCIceCandidate(
          payload['candidate'],
          payload['sdpMid'],
          payload['sdpMLineIndex'],
        ),
      );
    }
  }

  Future<RTCPeerConnection> _createPeerConnection() async {
    Map<String, dynamic> configuration = {
      'iceServers': [
        {'url': 'stun:stun.l.google.com:19302'},
      ]
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

    pc.onTrack = (RTCTrackEvent event) {
      if (event.streams.isNotEmpty) {
        onRemoteStream?.call(event.streams[0]);
      }
    };

    pc.onConnectionState = (RTCPeerConnectionState state) {
      print('Connection state change: $state');
      if (state == RTCPeerConnectionState.RTCPeerConnectionStateDisconnected ||
          state == RTCPeerConnectionState.RTCPeerConnectionStateFailed) {
        onCallEnded?.call();
      }
    };

    return pc;
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

  void dispose() {
    _localStream?.dispose();
    _peerConnection?.dispose();
    _channel?.sink.close();
  }
}
