#!/usr/bin/env bash
curl --fail-with-body --include \
  -H 'Content-Type: image/png' \
  --data-binary "@$(dirname "$0")/files/image.png" \
  "${BASE_URL:-http://localhost:8080}/image"
