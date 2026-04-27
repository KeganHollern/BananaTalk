import 'dart:typed_data';

import 'package:http/http.dart' as http;
import 'package:shared_preferences/shared_preferences.dart';

import 'logger_service.dart';

class ReportService {
  static const String _blockedKey = 'blocked_user_ids';

  final Uri endpoint;

  ReportService({required this.endpoint});

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
}
