import 'dart:convert';
import 'package:flutter_webrtc/flutter_webrtc.dart';
import 'package:web_socket_channel/web_socket_channel.dart';

typedef void OnLocalStream(MediaStream stream);
typedef void OnRemoteStream(MediaStream stream);

class Signaling {
  final String serverUrl;
  WebSocketChannel? _channel;
  RTCPeerConnection? _peerConnection;
  MediaStream? _localStream;
  
  OnLocalStream? onLocalStream;
  OnRemoteStream? onRemoteStream;
  
  String? _selfId;
  String? _remoteId;

  Signaling(this.serverUrl);

  Future<void> connect() async {
    _channel = WebSocketChannel.connect(Uri.parse(serverUrl));
    
    _channel!.stream.listen((message) {
      _handleMessage(jsonDecode(message));
    });
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
        _createOffer();
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
    }
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
      _send('ice_candidate', {
        'candidate': candidate.candidate,
        'sdpMid': candidate.sdpMid,
        'sdpMLineIndex': candidate.sdpMLineIndex,
      }, to: _remoteId);
    };

    pc.onTrack = (RTCTrackEvent event) {
      if (event.streams.isNotEmpty) {
        onRemoteStream?.call(event.streams[0]);
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

  void dispose() {
    _localStream?.dispose();
    _peerConnection?.dispose();
    _channel?.sink.close();
  }
}
