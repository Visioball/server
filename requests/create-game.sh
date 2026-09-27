#!/usr/bin/env bash
curl --fail-with-body --include \
  -H 'Content-Type: application/json' \
  --data '{"title":"Follow the sound","definition":{"mode":"followSound","durationSeconds":60}}' \
  "${BASE_URL:-http://localhost:8080}/games"
