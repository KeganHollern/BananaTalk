import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'services/logger_service.dart';

enum CallState { idle, matching, connected, reporting }

class CallStatus {
  final CallState state;
  const CallStatus(this.state);
}

class CallNotifier extends AutoDisposeNotifier<CallStatus> {
  @override
  CallStatus build() => const CallStatus(CallState.idle);

  void _transition(CallState next) {
    final prev = state.state;
    if (prev == next) return;
    LoggerService().logInfo('CallNotifier', 'State: $prev → $next');
    state = CallStatus(next);
  }

  void startMatching() => _transition(CallState.matching);
  void onConnected() => _transition(CallState.connected);
  void startReporting() => _transition(CallState.reporting);
  void endCall() => _transition(CallState.idle);
}

final callProvider =
    NotifierProvider.autoDispose<CallNotifier, CallStatus>(CallNotifier.new);
