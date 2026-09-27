#!/usr/bin/env bash
curl --fail-with-body --include \
  -H 'Content-Type: audio/wav' \
  --data-binary "@$(dirname "$0")/files/audio.wav" \
  "${BASE_URL:-http://localhost:8080}/audio"
