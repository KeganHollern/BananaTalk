import 'logger_service.dart';

enum PeerRole { offerer, answerer }

/// Records WebRTC connection-setup milestones for a single match attempt and
/// emits a structured report once the remote video is rendering. All durations
/// in [toReport] are measured in milliseconds from the moment the client
/// received the `match` message — that is the t0 the backend's
/// `bananatalk_connect_time_seconds` histogram is bucketed against.
class ConnectTiming {
  final DateTime queueJoinedAt;
  DateTime? matchAssignedAt;
  DateTime? offerCreatedAt;
  DateTime? offerSentAt;
  DateTime? answerReceivedAt;
  DateTime? remoteOfferReceivedAt;
  DateTime? answerSentAt;
  DateTime? iceGatheringCompleteAt;
  DateTime? firstRemoteTrackAt;
  DateTime? firstRemoteFrameAt;
  PeerRole? role;
  bool _reported = false;

  ConnectTiming() : queueJoinedAt = DateTime.now();

  bool get reported => _reported;

  int? _msSinceMatch(DateTime? t) {
    if (t == null || matchAssignedAt == null) return null;
    return t.difference(matchAssignedAt!).inMilliseconds;
  }

  Map<String, dynamic> toReport() {
    final report = <String, dynamic>{
      'role': role == PeerRole.offerer ? 'offerer' : 'answerer',
    };
    final queueWait = matchAssignedAt?.difference(queueJoinedAt).inMilliseconds;
    if (queueWait != null) report['queue_wait_ms'] = queueWait;

    void put(String key, int? v) {
      if (v != null) report[key] = v;
    }

    put('offer_created_ms', _msSinceMatch(offerCreatedAt));
    put('offer_sent_ms', _msSinceMatch(offerSentAt));
    put('answer_received_ms', _msSinceMatch(answerReceivedAt));
    put('remote_offer_received_ms', _msSinceMatch(remoteOfferReceivedAt));
    put('answer_sent_ms', _msSinceMatch(answerSentAt));
    put('ice_gathering_complete_ms', _msSinceMatch(iceGatheringCompleteAt));
    put('first_track_ms', _msSinceMatch(firstRemoteTrackAt));
    put('first_frame_ms', _msSinceMatch(firstRemoteFrameAt));
    return report;
  }

  /// Marks the report as sent so subsequent milestone updates (e.g. a late
  /// firstRemoteFrame) do not emit a duplicate.
  void markReported() {
    _reported = true;
    LoggerService().logInfo('ConnectTiming', 'reported: ${toReport()}');
  }
}
