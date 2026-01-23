import 'package:flutter/material.dart';
import 'package:flutter_webrtc/flutter_webrtc.dart';
import 'package:permission_handler/permission_handler.dart';

import 'signaling.dart';

void main() {
  runApp(const MyApp());
}

class MyApp extends StatelessWidget {
  const MyApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'BananaTalk',
      theme: ThemeData(
        brightness: Brightness.dark,
        primarySwatch: Colors.yellow,
        useMaterial3: true,
      ),
      home: const ChatScreen(),
    );
  }
}

// --- CONFIGURATION ---
// For Local Desktop: 'localhost'
// For Android Emulator: '10.0.2.2'
// For physical devices or iOS Simulator: Use your computer's local IP (e.g., '192.168.1.27')
const String serverAddress = 'localhost';
const String serverUrl = 'ws://$serverAddress:8080/ws';
// ---------------------

class ChatScreen extends StatefulWidget {
  const ChatScreen({super.key});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  final Signaling _signaling = Signaling(serverUrl);
  final RTCVideoRenderer _localRenderer = RTCVideoRenderer();
  final RTCVideoRenderer _remoteRenderer = RTCVideoRenderer();
  bool _inCall = false;

  @override
  void initState() {
    super.initState();
    initRenderers();

    _signaling.onLocalStream = (stream) {
      _localRenderer.srcObject = stream;
      setState(() {});
    };

    _signaling.onRemoteStream = (stream) {
      _remoteRenderer.srcObject = stream;
      setState(() {});
    };
  }

  Future<void> initRenderers() async {
    await _localRenderer.initialize();
    await _remoteRenderer.initialize();
  }

  @override
  void dispose() {
    _localRenderer.dispose();
    _remoteRenderer.dispose();
    _signaling.dispose();
    super.dispose();
  }

  void _join() async {
    Map<Permission, PermissionStatus> statuses = await [
      Permission.camera,
      Permission.microphone,
    ].request();

    if (statuses[Permission.camera]!.isGranted &&
        statuses[Permission.microphone]!.isGranted) {
      await _signaling.openUserMedia();
      await _signaling.connect();
      setState(() {
        _inCall = true;
      });
    } else {
      print('Permissions denied');
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: Stack(
        children: [
          // Remote Video (Background)
          Positioned.fill(
            child: Container(
              color: Colors.black,
              child: _remoteRenderer.srcObject != null
                  ? RTCVideoView(_remoteRenderer,
                      objectFit:
                          RTCVideoViewObjectFit.RTCVideoViewObjectFitCover)
                  : const Center(
                      child: Text(
                        'Waiting for partner...',
                        style: TextStyle(color: Colors.white54),
                      ),
                    ),
            ),
          ),

          // Local Video (PiP)
          Positioned(
            right: 20,
            top: 50,
            width: 120,
            height: 180,
            child: Container(
              decoration: BoxDecoration(
                color: Colors.black,
                borderRadius: BorderRadius.circular(12),
                border: Border.all(color: Colors.white24),
              ),
              clipBehavior: Clip.antiAlias,
              child: RTCVideoView(_localRenderer,
                  mirror: true,
                  objectFit: RTCVideoViewObjectFit.RTCVideoViewObjectFitCover),
            ),
          ),

          // UI Overlay
          if (!_inCall)
            Center(
              child: ElevatedButton(
                onPressed: _join,
                style: ElevatedButton.styleFrom(
                  backgroundColor: Colors.yellow,
                  foregroundColor: Colors.black,
                  padding:
                      const EdgeInsets.symmetric(horizontal: 40, vertical: 20),
                  textStyle: const TextStyle(
                      fontSize: 20, fontWeight: FontWeight.bold),
                ),
                child: const Text('JOIN CHAT'),
              ),
            ),

          if (_inCall)
            Positioned(
              bottom: 40,
              left: 0,
              right: 0,
              child: Center(
                child: Column(
                  children: [
                    const Text(
                      'Connected',
                      style: TextStyle(color: Colors.white, fontSize: 18),
                    ),
                    const SizedBox(height: 20),
                    IconButton(
                      onPressed: () {
                        // For Phase 1, we just close everything
                        _signaling.dispose();
                        setState(() {
                          _inCall = false;
                        });
                      },
                      icon: const Icon(Icons.call_end,
                          color: Colors.red, size: 40),
                    ),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }
}
