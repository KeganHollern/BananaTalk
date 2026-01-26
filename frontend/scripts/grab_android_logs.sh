#!/bin/bash

# Configuration
APP_PACKAGE="com.example.banana_talk"
# Standard Android path for internal/external files. 
# FlutterLogs typically writes to: /sdcard/Android/data/<package>/files/Logs
REMOTE_LOG_PATH="/sdcard/Android/data/$APP_PACKAGE/files/Logs"
LOCAL_LOG_DIR="local_logs"
TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
DEST_DIR="$LOCAL_LOG_DIR/$TIMESTAMP"

# add ~/Library/Android/sdk/platform-tools/ to path for ADB but only for this
export PATH=$PATH:~/Library/Android/sdk/platform-tools/

# Check for adb
if ! command -v adb &> /dev/null; then
    echo "Error: adb is not installed or not in PATH."
    exit 1
fi

# Check for jq
if ! command -v jq &> /dev/null; then
    echo "Error: jq is not installed. Please install jq to format logs."
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

    # Format logs with jq
    echo "Formatting logs with jq..."
    find "$DEST_DIR" -name "*.log" -print0 | while IFS= read -r -d '' file; do
        temp_file="${file}.tmp"
        # Use jq to pretty-print and remove unnecessary fields
        if jq 'del(.user, .organization, .host, .geo, .app)' "$file" > "$temp_file"; then
            mv "$temp_file" "$file"
            echo "Formatted: $file"
        else
            echo "Warning: Failed to format $file (might not be valid JSON). Keeping original."
            rm -f "$temp_file"
        fi
    done
else
    echo "Failed to pull logs. Please ensure the app has run and permissions are granted."
    echo "Note: On some Android versions, you may need 'run-as' access or debuggable build."
fi
