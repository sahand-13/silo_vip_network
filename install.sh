#!/usr/bin/env bash
# Downloadable entry point for installing this repository on a Linux server.
set -euo pipefail

repository="https://github.com/sahand-13/silo_vip_network.git"
branch="main"
install_dir="${SILO_INSTALL_DIR:-/opt/silo-vip-network}"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "Silo VIP Network requires a Linux server." >&2
  exit 1
fi
if [[ "$EUID" -ne 0 ]]; then
  echo "Run this installer with sudo or as root: sudo bash install-silo.sh" >&2
  exit 1
fi
for tool in git docker; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Missing $tool. Install Git, Docker Engine and Docker Compose before continuing." >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "Docker Engine must be running and the Docker Compose plugin must be installed." >&2
  exit 1
fi
if [[ "$install_dir" != /* || "$install_dir" == "/" ]]; then
  echo "SILO_INSTALL_DIR must be an absolute directory path other than /." >&2
  exit 1
fi

if [[ -e "$install_dir" ]]; then
  if [[ ! -d "$install_dir/.git" ]] ||
     [[ "$(git -C "$install_dir" remote get-url origin)" != "$repository" ]]; then
    echo "Installation directory exists but is not a clone of $repository: $install_dir" >&2
    exit 1
  fi
  if [[ -n "$(git -C "$install_dir" status --porcelain)" ]]; then
    echo "The installation directory has local changes. Resolve them before updating: $install_dir" >&2
    exit 1
  fi
  if [[ "$(git -C "$install_dir" branch --show-current)" != "$branch" ]]; then
    echo "The installation directory must be on the $branch branch." >&2
    exit 1
  fi
  git -C "$install_dir" pull --ff-only origin "$branch"
else
  mkdir -p "$(dirname -- "$install_dir")"
  git clone --branch "$branch" --single-branch "$repository" "$install_dir"
fi

bash "$install_dir/scripts/silo-server.sh" install
