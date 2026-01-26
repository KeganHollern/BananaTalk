import 'dart:io';

import 'package:flutter/foundation.dart';
import 'package:flutter_logs/flutter_logs.dart';

class LoggerService {
  static final LoggerService _instance = LoggerService._internal();

  factory LoggerService() {
    return _instance;
  }

  LoggerService._internal();

  Future<void> init() async {
    if (!Platform.isAndroid) return;

    // Initialize FlutterLogs
    await FlutterLogs.initLogs(
        logLevelsEnabled: [
          LogLevel.INFO,
          LogLevel.WARNING,
          LogLevel.ERROR,
          LogLevel.SEVERE
        ],
        timeStampFormat: TimeStampFormat.TIME_FORMAT_READABLE,
        directoryStructure: DirectoryStructure.FOR_DATE,
        logTypesEnabled: ["device", "network", "errors"],
        logFileExtension: LogFileExtension.LOG,
        logsWriteDirectoryName: "Logs",
        logsExportDirectoryName: "Logs/Export",
        debugFileOperations: kDebugMode);

    // Set retention to 1 day (API typically takes int days).
    // The library rotates files hourly, so we will benefit from that structure.
    // We cannot strictly set 1 hour retention via easy config usually,
    // but 1 day is a reasonable safe lower bound if int is required.
    // We will clean up manually if needed in the future.
    await FlutterLogs.setMetaInfo(
      appId: "banana_talk",
      appName: "BananaTalk",
      appVersion: "1.0",
      language: "en",
      // Fix for NumberFormatException in flutter_logs:
      // The native Android implementation tries to parse these as doubles.
      // If empty (default), it crashes.
      latitude: "0.0",
      longitude: "0.0",
    );

    // Configure default error handlers
    FlutterError.onError = (FlutterErrorDetails details) {
      logError("FlutterError", details.exceptionAsString(), details.exception,
          details.stack);

      // Pass to original handler if needed, or just dump to console too
      FlutterError.presentError(details);
    };

    PlatformDispatcher.instance.onError = (error, stack) {
      logError("AsyncError", error.toString(), error, stack);
      return true;
    };
  }

  void logError(
      String tag, String message, dynamic error, StackTrace? stackTrace) {
    if (!Platform.isAndroid) {
      print("[$tag] $message");
      if (error != null) print(error);
      if (stackTrace != null) print(stackTrace);
      return;
    }

    FlutterLogs.logThis(
        tag: tag,
        subTag: "Error",
        logMessage: "$message\nError: $error\nStack: $stackTrace",
        level: LogLevel.ERROR);
  }

  void logInfo(String tag, String message) {
    if (!Platform.isAndroid) {
      print("[$tag] $message");
      return;
    }
    FlutterLogs.logThis(
        tag: tag, subTag: "Info", logMessage: message, level: LogLevel.INFO);
  }
}
