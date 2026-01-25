import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_webrtc/flutter_webrtc.dart';
import 'package:google_sign_in/google_sign_in.dart';
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
      home: const LoginScreen(),
    );
  }
}

// --- CONFIGURATION ---
// For Local Desktop: 'localhost'
// For Android Emulator: '10.0.2.2'
// For physical devices or iOS Simulator: Use your computer's local IP (e.g., '192.168.1.27')
const String serverUrl = 'wss://bt.lystic.dev/ws';
// ---------------------

class ChatScreen extends StatefulWidget {
  final String token;
  const ChatScreen({super.key, required this.token});

  @override
  State<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends State<ChatScreen> {
  late final Signaling _signaling;
  final RTCVideoRenderer _localRenderer = RTCVideoRenderer();
  final RTCVideoRenderer _remoteRenderer = RTCVideoRenderer();
  bool _inCall = false;

  @override
  void initState() {
    super.initState();
    _signaling = Signaling(serverUrl, widget.token);
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
    // on macOS, permissions are handled by the OS and flutter_webrtc automatically.
    // permission_handler doesn't need to be used and causes MissingPluginException
    if (Platform.isMacOS) {
      await _signaling.openUserMedia();
      await _signaling.connect();
      setState(() {
        _inCall = true;
      });
      return;
    }

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

// android client id
const String webClientId =
    '774973448609-nio4jsbacsm16irumk21v66js92d8j2e.apps.googleusercontent.com';

class LoginScreen extends StatefulWidget {
  const LoginScreen({super.key});

  @override
  State<LoginScreen> createState() => _LoginScreenState();
}

class _LoginScreenState extends State<LoginScreen> {
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _initAndCheckSignIn();
  }

  Future<void> _initAndCheckSignIn() async {
    try {
      // Must initialize first
      if (Platform.isAndroid) {
        await GoogleSignIn.instance.initialize(
          serverClientId: webClientId,
        );
      } else {
        await GoogleSignIn.instance.initialize();
      }

      var account =
          await GoogleSignIn.instance.attemptLightweightAuthentication();
      if (account != null) {
        _handleSignIn(account);
      } else {
        setState(() {
          _loading = false;
        });
      }
    } catch (e) {
      print('Silent sign in error: $e');
      setState(() {
        _loading = false;
      });
    }
  }

  Future<void> _handleSignIn(GoogleSignInAccount account) async {
    try {
      final auth = account.authentication;
      final idToken = auth.idToken;

      if (idToken != null) {
        if (!mounted) return;
        Navigator.of(context).pushReplacement(
          MaterialPageRoute(
            builder: (_) => ChatScreen(token: idToken),
          ),
        );
      } else {
        // Handle missing token...
        print('ID Token is null');
        setState(() {
          _loading = false;
        });
      }
    } catch (e) {
      print('Auth details error: $e');
      setState(() {
        _loading = false;
      });
    }
  }

  Future<void> _signIn() async {
    try {
      var account = await GoogleSignIn.instance.authenticate();
      _handleSignIn(account);
    } catch (e) {
      print('Sign in error: $e');
    }
  }

  @override
  Widget build(BuildContext context) {
    if (_loading) {
      return const Scaffold(
        body: Center(child: CircularProgressIndicator()),
      );
    }

    return Scaffold(
      appBar: AppBar(title: const Text('BananaTalk Login')),
      body: Center(
        child: Column(
          mainAxisAlignment: MainAxisAlignment.center,
          children: [
            const Text(
              'Welcome to BananaTalk',
              style: TextStyle(fontSize: 24, fontWeight: FontWeight.bold),
            ),
            const SizedBox(height: 30),
            ElevatedButton.icon(
              onPressed: _signIn,
              icon: const Icon(Icons.login),
              label: const Text('Sign in with Google'),
              style: ElevatedButton.styleFrom(
                backgroundColor: Colors.white,
                foregroundColor: Colors.black,
                padding:
                    const EdgeInsets.symmetric(horizontal: 24, vertical: 12),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
