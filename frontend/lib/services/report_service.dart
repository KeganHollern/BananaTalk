import 'dart:convert';
import 'dart:typed_data';

import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';

import 'logger_service.dart';

class ReportService {
  static const String _blockedKey = 'blocked_user_ids';

  final Uri endpoint;
  final Uri blocksEndpoint;
  final http.Client _client;

  ReportService({
    required this.endpoint,
    required this.blocksEndpoint,
    http.Client? client,
  }) : _client = client ?? http.Client();

  Future<bool> submit({
    required String token,
    required String reportedUserId,
    required String reason,
    required Uint8List frameBytes,
    required String filename,
  }) async {
    final request = http.MultipartRequest('POST', endpoint)
      ..headers['Authorization'] = 'Bearer $token'
      ..fields['reported_user_id'] = reportedUserId
      ..fields['reason'] = reason
      ..files.add(http.MultipartFile.fromBytes(
        'screenshot',
        frameBytes,
        filename: filename,
      ));

    try {
      final streamed = await request.send();
      final ok = streamed.statusCode >= 200 && streamed.statusCode < 300;
      if (!ok) {
        final body = await streamed.stream.bytesToString();
        LoggerService().logError(
          'ReportService',
          'Report failed: ${streamed.statusCode} $body',
          null,
          StackTrace.current,
        );
      }
      return ok;
    } catch (e, s) {
      LoggerService().logError('ReportService', 'Upload exception', e, s);
      return false;
    }
  }

  Future<void> block(String userId) async {
    final prefs = await SharedPreferences.getInstance();
    final list = prefs.getStringList(_blockedKey) ?? <String>[];
    if (!list.contains(userId)) {
      list.add(userId);
      await prefs.setStringList(_blockedKey, list);
    }
  }

  Future<List<String>> blockedList() async {
    final prefs = await SharedPreferences.getInstance();
    return prefs.getStringList(_blockedKey) ?? <String>[];
  }

  /// Pulls the authoritative block list from the server and unions it into
  /// SharedPrefs. The local list is never shrunk on sync — a block recorded
  /// while offline must survive until it can be flushed to the server.
  ///
  /// Network or server failures are swallowed: launch must not be blocked
  /// when the user is offline or the backend is briefly unreachable. The
  /// matchmaker is server-authoritative, so a missed sync is degraded UX
  /// rather than a correctness hazard.
  Future<void> syncFromServer({required String token}) async {
    try {
      final res = await _client.get(
        blocksEndpoint,
        headers: {'Authorization': 'Bearer $token'},
      );
      if (res.statusCode < 200 || res.statusCode >= 300) {
        LoggerService().logError(
          'ReportService',
          'Block sync failed: ${res.statusCode} ${res.body}',
          null,
          StackTrace.current,
        );
        return;
      }
      final body = jsonDecode(res.body);
      if (body is! Map<String, dynamic>) return;
      final raw = body['blocked_ids'];
      if (raw is! List) return;
      final remote = raw.whereType<String>().toSet();

      final prefs = await SharedPreferences.getInstance();
      final local = (prefs.getStringList(_blockedKey) ?? <String>[]).toSet();
      final merged = {...local, ...remote}.toList();
      await prefs.setStringList(_blockedKey, merged);
    } catch (e, s) {
      LoggerService().logError('ReportService', 'Block sync exception', e, s);
    }
  }
}
