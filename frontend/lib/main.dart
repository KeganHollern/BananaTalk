import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
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
  late Signaling _signaling;
  final RTCVideoRenderer _localRenderer = RTCVideoRenderer();
  final RTCVideoRenderer _remoteRenderer = RTCVideoRenderer();
  bool _inCall = false;

  @override
  void initState() {
    super.initState();
    _connect(widget.token);
    initRenderers();
  }

  void _connect(String token) {
    _signaling = Signaling(serverUrl, token);

    _signaling.onConnectionError = (error) {
      print('Connection error: $error. Attempting refresh...');
      _handleConnectionError();
    };

    _signaling.onLocalStream = (stream) {
      _localRenderer.srcObject = stream;
      setState(() {});
    };

    _signaling.onRemoteStream = (stream) {
      _remoteRenderer.srcObject = stream;
      setState(() {});
    };

    _signaling.onCallEnded = () {
      if (!mounted) return;
      _signaling.dispose();
      setState(() {
        _inCall = false;
        _remoteRenderer.srcObject = null;
      });
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Call ended')),
      );
    };
  }

  Future<void> _handleConnectionError() async {
    try {
      // Attempt silent refresh
      final account =
          await GoogleSignIn.instance.attemptLightweightAuthentication();
      if (account != null) {
        final auth = account.authentication;
        final newToken = auth.idToken;
        if (newToken != null) {
          print('Token refreshed successfully. Reconnecting...');
          // Update storage
          final storage = FlutterSecureStorage();
          await storage.write(key: 'auth_token', value: newToken);

          // Reconnect with new token
          _signaling.dispose();
          // Slight delay to ensure socket cleanup?
          _connect(newToken);
          // If we were trying to join, we might need to retry 'connect' and 'openUserMedia'?
          // But wait, _connect here just sets up the object.
          // The actual "Join" button calls _join() which calls _signaling.connect().
          // If we are ALREADY in a call?
          // If we are in a call and it drops, the UI state _inCall might still be true?
          // If the socket drops, we probably want to try to recover the session or just go back to "Join" state.
          // For simplicity, let's just re-instantiate signaling. Use will have to click "Join" again if they weren't in call.
          // If they WERE in call, we probably dropped the call.
        } else {
          _handleAuthFailure();
        }
      } else {
        _handleAuthFailure();
      }
    } catch (e) {
      print('Refresh failed: $e');
      _handleAuthFailure();
    }
  }

  void _handleAuthFailure() async {
    print('Auth failure. Redirecting to login.');
    final storage = FlutterSecureStorage();
    await storage.delete(key: 'auth_token');

    if (mounted) {
      Navigator.of(context).pushReplacement(
        MaterialPageRoute(builder: (_) => const LoginScreen()),
      );
    }
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
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  ElevatedButton(
                    onPressed: _join,
                    style: ElevatedButton.styleFrom(
                      backgroundColor: Colors.yellow,
                      foregroundColor: Colors.black,
                      padding: const EdgeInsets.symmetric(
                          horizontal: 40, vertical: 20),
                      textStyle: const TextStyle(
                          fontSize: 20, fontWeight: FontWeight.bold),
                    ),
                    child: const Text('JOIN CHAT'),
                  ),
                  const SizedBox(height: 20),
                  TextButton(
                    onPressed: () async {
                      final storage = FlutterSecureStorage();
                      await storage.delete(key: 'auth_token');
                      try {
                        await GoogleSignIn.instance.signOut();
                      } catch (e) {
                        print('Sign out error: $e');
                      }

                      if (context.mounted) {
                        Navigator.of(context).pushReplacement(
                          MaterialPageRoute(
                            builder: (_) => const LoginScreen(),
                          ),
                        );
                      }
                    },
                    style:
                        TextButton.styleFrom(foregroundColor: Colors.white70),
                    child: const Text('Sign Out'),
                  ),
                ],
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
                        _signaling.sendBye();
                        _signaling.dispose();
                        setState(() {
                          _inCall = false;
                          _remoteRenderer.srcObject = null;
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
  final _storage = FlutterSecureStorage();
  bool _loading = true;

  @override
  void initState() {
    super.initState();
    _initAndCheckSignIn();
  }

  Future<void> _initAndCheckSignIn() async {
    try {
      // 1. Check local storage first
      String? cachedToken = await _storage.read(key: 'auth_token');
      if (cachedToken != null) {
        print('Found cached token, skipping Google Sign-In init');
        _navigateToChat(cachedToken);
        return;
      }

      // 2. Initialize Google Sign-In if no token found
      if (Platform.isAndroid) {
        await GoogleSignIn.instance.initialize(
          serverClientId: webClientId,
        );
      } else {
        await GoogleSignIn.instance.initialize();
      }

      // 3. Try silent sign-in (only if we didn't have a token)
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

  void _navigateToChat(String token) {
    if (!mounted) return;
    Navigator.of(context).pushReplacement(
      MaterialPageRoute(
        builder: (_) => ChatScreen(token: token),
      ),
    );
  }

  Future<void> _handleSignIn(GoogleSignInAccount account) async {
    try {
      final auth = account.authentication;
      final idToken = auth.idToken;

      if (idToken != null) {
        await _storage.write(key: 'auth_token', value: idToken);
        _navigateToChat(idToken);
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
