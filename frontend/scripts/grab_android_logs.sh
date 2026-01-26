#!/bin/bash

# Configuration
APP_PACKAGE="com.example.banana_talk"
# Standard Android path for internal/external files. 
# FlutterLogs typically writes to: /sdcard/Android/data/<package>/files/Logs
REMOTE_LOG_PATH="/sdcard/Android/data/$APP_PACKAGE/files/Logs"
LOCAL_LOG_DIR="local_logs"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
DEST_DIR="$LOCAL_LOG_DIR/$TIMESTAMP"

# Check for adb
if ! command -v adb &> /dev/null; then
    echo "Error: adb is not installed or not in PATH."
    exit 1
fi

# Check for connected device
DEVICE_COUNT=$(adb devices | grep -E "\tdevice$" | wc -l)
if [ "$DEVICE_COUNT" -eq 0 ]; then
    echo "Error: No Android device connected."
    exit 1
fi

echo "Found $DEVICE_COUNT device(s)."

# Create local directory
mkdir -p "$DEST_DIR"

echo "Pulling logs from $REMOTE_LOG_PATH..."

# Attempt to pull
adb pull "$REMOTE_LOG_PATH/." "$DEST_DIR/"

if [ $? -eq 0 ]; then
    echo "Logs successfully pulled to: $DEST_DIR"
    
    # List files
    echo "Files retrieved:"
    ls -lh "$DEST_DIR"
else
    echo "Failed to pull logs. Please ensure the app has run and permissions are granted."
    echo "Note: On some Android versions, you may need 'run-as' access or debuggable build."
fi
