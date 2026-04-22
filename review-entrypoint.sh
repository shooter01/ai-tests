#!/usr/bin/env bash
set -euo pipefail

: "${GIT_CLONE_URL:?GIT_CLONE_URL is required}"
: "${GIT_BRANCH:?GIT_BRANCH is required}"
: "${GIGACHAT_TOKEN:?GIGACHAT_TOKEN is required}"
: "${OPENCODE_MODEL:?OPENCODE_MODEL is required}"
: "${REVIEW_PROMPT:?REVIEW_PROMPT is required}"

WORKSPACE=/workspace
mkdir -p "$WORKSPACE"
cd "$WORKSPACE"

echo "[entrypoint] git clone branch=$GIT_BRANCH" >&2
git clone --depth 1 --single-branch --branch "$GIT_BRANCH" "$GIT_CLONE_URL" repo 2> >(sed "s#${GIT_CLONE_URL}#<CLONE_URL>#g" >&2)

cd repo
echo "[entrypoint] files: $(find . -type f | wc -l), size: $(du -sh . | cut -f1)" >&2

cp /config/opencode.json ./opencode.json
cp -r /config/agents ./agents

exec opencode run \
  --dangerously-skip-permissions \
  --agent review \
  --model "$OPENCODE_MODEL" \
  "$REVIEW_PROMPT"