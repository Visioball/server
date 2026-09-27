#!/usr/bin/env bash
# Usage: bash requests/get-game.sh <id returned by create-game.sh>
curl --fail-with-body --include \
  "${BASE_URL:-http://localhost:8080}/games/${1:?Pass the game ID returned by create-game.sh}"
