import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_webrtc/flutter_webrtc.dart';
import 'package:google_sign_in/google_sign_in.dart';
import 'package:jwt_decoder/jwt_decoder.dart';
import 'package:permission_handler/permission_handler.dart';

import 'call_state.dart';
import 'services/logger_service.dart';
import 'services/report_service.dart';
import 'signaling.dart';

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await LoggerService().init();
  runApp(const ProviderScope(child: MyApp()));
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
const String reportUrl = 'https://bt.lystic.dev/report';
// ---------------------

class ChatScreen extends ConsumerStatefulWidget {
  final String token;
  const ChatScreen({super.key, required this.token});

  @override
  ConsumerState<ChatScreen> createState() => _ChatScreenState();
}

class _ChatScreenState extends ConsumerState<ChatScreen>
    with SingleTickerProviderStateMixin {
  late Signaling _signaling;
  final RTCVideoRenderer _localRenderer = RTCVideoRenderer();
  final RTCVideoRenderer _remoteRenderer = RTCVideoRenderer();
  final ReportService _reportService =
      ReportService(endpoint: Uri.parse(reportUrl));
  final GlobalKey _remoteVideoKey = GlobalKey();

  /// When non-null, painted over the remote video to "freeze" it during the
  /// reporting flow.
  Uint8List? _frozenFrame;

  late AnimationController _slideController;
  late Animation<Offset> _outgoingSlide;
  late Animation<Offset> _incomingSlide;
  bool _isSliding = false;

  bool _micEnabled = true;
  bool _camEnabled = true;

  @override
  void initState() {
    super.initState();
    _slideController = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 350),
    );
    _outgoingSlide = Tween<Offset>(
      begin: Offset.zero,
      end: const Offset(0, -1),
    ).animate(CurvedAnimation(
      parent: _slideController,
      curve: Curves.easeInOut,
    ));
    _incomingSlide = Tween<Offset>(
      begin: const Offset(0, 1),
      end: Offset.zero,
    ).animate(CurvedAnimation(
      parent: _slideController,
      curve: Curves.easeInOut,
    ));
    _connect(widget.token);
    _initRenderers();
  }

  void _connect(String token) {
    _signaling = Signaling(serverUrl, token);

    _signaling.onConnectionError = (error) {
      LoggerService().logError(
          'ChatScreen', 'Connection error', error, StackTrace.current);
      if (!mounted) return;
      ref.read(callProvider.notifier).endCall();
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Connection lost: $error')),
      );
    };

    _signaling.onLocalStream = (stream) {
      setState(() {
        _localRenderer.srcObject = stream;
      });
    };

    _signaling.onRemoteStream = (stream) {
      setState(() {
        _remoteRenderer.srcObject = stream;
      });
      if (mounted) {
        ref.read(callProvider.notifier).onConnected();
      }
    };

    _signaling.onCallEnded = () {
      if (!mounted) return;
      final s = ref.read(callProvider).state;
      // Already idle or re-entering match queue — nothing to do.
      if (s == CallState.idle || s == CallState.matching) return;
      _signaling.dispose();
      setState(() {
        _remoteRenderer.srcObject = null;
      });
      ref.read(callProvider.notifier).endCall();
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Call ended')),
      );
    };
  }

  Future<void> _initRenderers() async {
    await _localRenderer.initialize();
    await _remoteRenderer.initialize();
  }

  @override
  void dispose() {
    _slideController.dispose();
    _localRenderer.dispose();
    _remoteRenderer.dispose();
    _signaling.dispose();
    super.dispose();
  }

  void _onVerticalDragEnd(DragEndDetails details) {
    final velocity = details.primaryVelocity ?? 0;
    if (velocity < -300) {
      _swipeToNext();
    }
  }

  Future<void> _swipeToNext() async {
    HapticFeedback.lightImpact();
    _signaling.findNextMatch();

    setState(() => _isSliding = true);
    await _slideController.forward();
    _slideController.reset();

    if (!mounted) return;
    setState(() {
      _remoteRenderer.srcObject = null;
      _isSliding = false;
    });
    ref.read(callProvider.notifier).startMatching();
  }

  void _join() async {
    if (Platform.isMacOS) {
      await _signaling.openUserMedia();
      await _signaling.connect();
      ref.read(callProvider.notifier).startMatching();
      return;
    }

    final statuses = await [
      Permission.camera,
      Permission.microphone,
    ].request();

    if (statuses[Permission.camera]!.isGranted &&
        statuses[Permission.microphone]!.isGranted) {
      await _signaling.openUserMedia();
      await _signaling.connect();
      ref.read(callProvider.notifier).startMatching();
    } else {
      LoggerService().logInfo('ChatScreen', 'Permissions denied');
    }
  }

  Future<Uint8List?> _captureRemoteFrame() async {
    final stream = _remoteRenderer.srcObject;
    final track = stream?.getVideoTracks().isNotEmpty == true
        ? stream!.getVideoTracks().first
        : null;
    if (track != null) {
      try {
        final buffer = await track.captureFrame();
        return buffer.asUint8List();
      } catch (e, s) {
        LoggerService()
            .logError('ChatScreen', 'captureFrame failed, falling back', e, s);
      }
    }

    try {
      final ctx = _remoteVideoKey.currentContext;
      if (ctx == null) return null;
      // ignore: use_build_context_synchronously
      final boundary = ctx.findRenderObject() as RenderRepaintBoundary?;
      if (boundary == null) return null;
      final ui.Image image = await boundary.toImage(pixelRatio: 1.0);
      final ByteData? data =
          await image.toByteData(format: ui.ImageByteFormat.png);
      return data?.buffer.asUint8List();
    } catch (e, s) {
      LoggerService()
          .logError('ChatScreen', 'RepaintBoundary capture failed', e, s);
      return null;
    }
  }

  Future<void> _onReportTap() async {
    final reportedId = _signaling.remoteId;
    if (reportedId == null) return;

    ref.read(callProvider.notifier).startReporting();
    final frame = await _captureRemoteFrame();
    if (!mounted) return;
    if (frame == null) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(content: Text('Could not capture frame for report.')),
      );
      ref.read(callProvider.notifier).onConnected();
      return;
    }
    setState(() => _frozenFrame = frame);

    final reason = await _showReasonSheet();
    if (!mounted) return;

    if (reason == null) {
      // User cancelled — unfreeze and return to connected.
      setState(() => _frozenFrame = null);
      ref.read(callProvider.notifier).onConnected();
      return;
    }

    final ok = await _reportService.submit(
      token: widget.token,
      reportedUserId: reportedId,
      reason: reason,
      frameBytes: frame,
      filename: 'screenshot.jpg',
    );
    await _reportService.block(reportedId);

    if (!mounted) return;
    ScaffoldMessenger.of(context).showSnackBar(
      SnackBar(
        content: Text(ok
            ? 'User reported. Finding new match...'
            : 'Report failed to upload. Finding new match...'),
      ),
    );

    setState(() => _frozenFrame = null);
    await _swipeToNext();
  }

  Future<String?> _showReasonSheet() {
    const reasons = <String>[
      'Nudity',
      'Harassment',
      'Underage',
      'Spam',
      'Other',
    ];
    return showModalBottomSheet<String>(
      context: context,
      backgroundColor: const Color(0xFF1C1C1E),
      isDismissible: true,
      enableDrag: true,
      shape: const RoundedRectangleBorder(
        borderRadius: BorderRadius.vertical(top: Radius.circular(16)),
      ),
      builder: (sheetCtx) {
        return SafeArea(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Padding(
                padding: EdgeInsets.fromLTRB(20, 16, 20, 8),
                child: Text(
                  'Reason for reporting?',
                  style: TextStyle(
                    color: Colors.white,
                    fontSize: 18,
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ),
              for (final r in reasons)
                ListTile(
                  title: Text(r,
                      style: const TextStyle(color: Colors.white)),
                  onTap: () => Navigator.of(sheetCtx).pop(r),
                ),
              const SizedBox(height: 8),
            ],
          ),
        );
      },
    );
  }

  void _toggleMic() {
    final stream = _localRenderer.srcObject;
    final tracks = stream?.getAudioTracks();
    if (tracks == null || tracks.isEmpty) return;
    final next = !tracks.first.enabled;
    tracks.first.enabled = next;
    setState(() => _micEnabled = next);
  }

  void _toggleCamera() {
    final stream = _localRenderer.srcObject;
    final tracks = stream?.getVideoTracks();
    if (tracks == null || tracks.isEmpty) return;
    final next = !tracks.first.enabled;
    tracks.first.enabled = next;
    setState(() => _camEnabled = next);
  }

  void _endCall() {
    _signaling.sendBye();
    _signaling.dispose();
    setState(() {
      _remoteRenderer.srcObject = null;
    });
    ref.read(callProvider.notifier).endCall();
  }

  Future<void> _signOut() async {
    const storage = FlutterSecureStorage();
    await storage.delete(key: 'auth_token');
    try {
      await GoogleSignIn.instance.signOut();
    } catch (e, stack) {
      LoggerService().logError('ChatScreen', 'Sign out error', e, stack);
    }
    if (!mounted) return;
    Navigator.of(context).pushReplacement(
      MaterialPageRoute(builder: (_) => const LoginScreen()),
    );
  }

  /// Returns content shown in the black background area when no remote stream.
  Widget _buildBackgroundContent(CallState state) {
    return switch (state) {
      CallState.idle => const Text(
          'Waiting for partner...',
          style: TextStyle(color: Colors.white54),
        ),
      CallState.matching => const Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            CircularProgressIndicator(color: Colors.yellow),
            SizedBox(height: 16),
            Text(
              'Finding a partner...',
              style: TextStyle(color: Colors.white54),
            ),
          ],
        ),
      CallState.connected => const SizedBox.shrink(),
      CallState.reporting => const Text(
          'Submitting report...',
          style: TextStyle(color: Colors.white54),
        ),
    };
  }

  /// Returns the foreground overlay widget driven entirely by [CallState].
  Widget _buildOverlay(CallState state) {
    return switch (state) {
      CallState.idle => Center(
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
                onPressed: _signOut,
                style: TextButton.styleFrom(foregroundColor: Colors.white70),
                child: const Text('Sign Out'),
              ),
            ],
          ),
        ),
      CallState.matching => const SizedBox.shrink(),
      CallState.connected => Positioned(
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
                Row(
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    _CircleToolbarButton(
                      tooltip: _micEnabled ? 'Mute microphone' : 'Unmute microphone',
                      icon: _micEnabled ? Icons.mic : Icons.mic_off,
                      onPressed: _toggleMic,
                    ),
                    const SizedBox(width: 24),
                    IconButton(
                      onPressed: _endCall,
                      icon: const Icon(Icons.call_end, color: Colors.red, size: 40),
                    ),
                    const SizedBox(width: 24),
                    _CircleToolbarButton(
                      tooltip: _camEnabled ? 'Turn camera off' : 'Turn camera on',
                      icon: _camEnabled ? Icons.videocam : Icons.videocam_off,
                      onPressed: _toggleCamera,
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      CallState.reporting => const SizedBox.shrink(),
    };
  }

  @override
  Widget build(BuildContext context) {
    final callStatus = ref.watch(callProvider);

    return Scaffold(
      body: GestureDetector(
        // Only active during a live call; upward velocity > 300 triggers next.
        onVerticalDragEnd: callStatus.state == CallState.connected
            ? _onVerticalDragEnd
            : null,
        child: Stack(
          children: [
            // Remote Video / Background content (with slide-up transition)
            Positioned.fill(
              child: Container(
                color: Colors.black,
                child: _isSliding
                    ? ClipRect(
                        child: Stack(
                          fit: StackFit.expand,
                          children: [
                            // Incoming: matching spinner slides in from below.
                            SlideTransition(
                              position: _incomingSlide,
                              child: Center(
                                child: _buildBackgroundContent(
                                    CallState.matching),
                              ),
                            ),
                            // Outgoing: current video slides off to the top.
                            SlideTransition(
                              position: _outgoingSlide,
                              child: RTCVideoView(
                                _remoteRenderer,
                                objectFit: RTCVideoViewObjectFit
                                    .RTCVideoViewObjectFitCover,
                              ),
                            ),
                          ],
                        ),
                      )
                    : (_remoteRenderer.srcObject != null
                        ? Stack(
                            fit: StackFit.expand,
                            children: [
                              RepaintBoundary(
                                key: _remoteVideoKey,
                                child: RTCVideoView(
                                  _remoteRenderer,
                                  objectFit: RTCVideoViewObjectFit
                                      .RTCVideoViewObjectFitCover,
                                ),
                              ),
                              if (_frozenFrame != null)
                                Image.memory(
                                  _frozenFrame!,
                                  fit: BoxFit.cover,
                                  gaplessPlayback: true,
                                ),
                            ],
                          )
                        : Center(
                            child: _buildBackgroundContent(callStatus.state),
                          )),
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
                child: RTCVideoView(
                  _localRenderer,
                  mirror: true,
                  objectFit: RTCVideoViewObjectFit.RTCVideoViewObjectFitCover,
                ),
              ),
            ),

            // Report shield — only visible during a live, connected call.
            if (!_isSliding && callStatus.state == CallState.connected)
              Positioned(
                top: 50,
                left: 20,
                child: Material(
                  color: Colors.black54,
                  shape: const CircleBorder(),
                  clipBehavior: Clip.antiAlias,
                  child: IconButton(
                    tooltip: 'Report user',
                    icon: const Icon(Icons.shield_outlined,
                        color: Colors.white, size: 22),
                    onPressed: _onReportTap,
                  ),
                ),
              ),

            // UI Overlay — hidden during the slide animation.
            if (!_isSliding) _buildOverlay(callStatus.state),
          ],
        ),
      ),
    );
  }
}

class _CircleToolbarButton extends StatelessWidget {
  final String tooltip;
  final IconData icon;
  final VoidCallback onPressed;

  const _CircleToolbarButton({
    required this.tooltip,
    required this.icon,
    required this.onPressed,
  });

  @override
  Widget build(BuildContext context) {
    return Material(
      color: Colors.black54,
      shape: const CircleBorder(),
      clipBehavior: Clip.antiAlias,
      child: IconButton(
        tooltip: tooltip,
        icon: Icon(icon, color: Colors.white, size: 22),
        onPressed: onPressed,
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
  final _storage = const FlutterSecureStorage();
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
        bool isExpired = JwtDecoder.isExpired(cachedToken);
        if (isExpired) {
          LoggerService()
              .logInfo('LoginScreen', 'Cached token expired, clearing...');
          await _storage.delete(key: 'auth_token');
          cachedToken = null;
        } else {
          LoggerService().logInfo('LoginScreen',
              'Found cached token, skipping Google Sign-In init');
          _navigateToChat(cachedToken);
          return;
        }
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
    } catch (e, stack) {
      LoggerService().logError('LoginScreen', 'Silent sign in error', e, stack);
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
        LoggerService().logError(
            'LoginScreen', 'ID Token is null', null, StackTrace.current);
        setState(() {
          _loading = false;
        });
      }
    } catch (e, stack) {
      LoggerService().logError('LoginScreen', 'Auth details error', e, stack);
      setState(() {
        _loading = false;
      });
    }
  }

  Future<void> _signIn() async {
    try {
      var account = await GoogleSignIn.instance.authenticate();
      _handleSignIn(account);
    } catch (e, stack) {
      LoggerService().logError('LoginScreen', 'Sign in error', e, stack);
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
