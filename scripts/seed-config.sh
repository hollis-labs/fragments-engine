#!/usr/bin/env bash
# Seed the gitignored runtime config from the committed template on first deploy.
#
# fragments.example.yaml is a hand-maintained, commented template. It must never
# be used as the live `--config` path: the ingest CRUD endpoints
# (POST /v1/ingests/create|update|delete|set-enabled) rewrite the config file in
# place via Go's YAML marshaller, which strips comments, re-indents, reorders
# keys, and adds machine-default fields. Pointing the server at the template
# clobbers it into machine output.
#
# This script copies the template to the runtime path once, then leaves the
# runtime file alone on every subsequent run, so it is safe to call on each
# deploy. The runtime path is gitignored (see .gitignore).
#
# Usage: scripts/seed-config.sh [template] [runtime]
#   template  defaults to fragments.example.yaml
#   runtime   defaults to fragments.yaml
set -euo pipefail

template="${1:-fragments.example.yaml}"
runtime="${2:-fragments.yaml}"

if [[ -f "$runtime" ]]; then
  echo "seed-config: runtime config already present, leaving as-is: $runtime"
  exit 0
fi

if [[ ! -f "$template" ]]; then
  echo "seed-config: template not found: $template" >&2
  exit 1
fi

runtime_dir="$(dirname "$runtime")"
mkdir -p "$runtime_dir"
cp "$template" "$runtime"
echo "seed-config: seeded runtime config $runtime from $template"
