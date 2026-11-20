#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This server installation requires Linux and Docker host networking." >&2
  exit 1
fi
if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  echo "Docker Engine and the Docker Compose plugin are required." >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Cannot access Docker. Start Docker and run this script with an authorized account (for example, sudo)." >&2
  exit 1
fi

compose=(docker compose -f "$project_dir/docker-compose.silo.yml" -p silo-vip-network)
case "${1:-install}" in
  install|update)
    # Build before replacing the running container; never delete existing volumes.
    "${compose[@]}" build
    "${compose[@]}" up -d --no-build
    "${compose[@]}" ps
    echo
    echo "On a first installation, find the generated login in:"
    echo "  sudo docker compose -f $project_dir/docker-compose.silo.yml -p silo-vip-network logs --tail=100 silo"
    echo "The default panel port is 2053. See docs/silo-server-install.fa.md."
    ;;
  status)
    "${compose[@]}" ps
    ;;
  logs)
    "${compose[@]}" logs --tail=100 -f silo
    ;;
  *)
    echo "Usage: bash scripts/silo-server.sh [install|update|status|logs]" >&2
    exit 2
    ;;
esac
