# Silo VIP Network

[فارسی](README.fa_IR.md) | English

Silo VIP Network is a web panel for managing Xray-based services and delegated client accounts. It is a fork of [3X-UI](https://github.com/MHSanaei/3x-ui) with account hierarchy, reseller quotas, client ownership and per-account Telegram backups.

[Releases](https://github.com/sahand-13/silo_vip_network/releases) · [Issues](https://github.com/sahand-13/silo_vip_network/issues) · [Persian server guide](docs/silo-server-install.fa.md)

## Features

- Manage panel accounts in a hierarchy with owner, sudo, admin, reseller and infinity roles. Available actions depend on the account's role and assigned permissions.
- Allocate client slots and traffic to delegated accounts; optionally grant unlimited client slots or a specified number of unlimited-traffic clients.
- Manage each account's permitted inbounds and clients, including client settings, traffic, expiration, subscription links and QR codes. Infinity accounts can also manage clients in their descendant hierarchy.
- Transfer client ownership when the account has the required permission. Client quota accounting and return of remaining traffic depend on the operation and account policy.
- Configure individual Telegram backup settings for delegated accounts. The panel also supports Telegram bot connections through a proxy or a configured outbound.
- Manage Xray inbounds, outbounds, routing, client usage and subscriptions through a multilingual interface with light and dark themes.
- Use SQLite by default or configure PostgreSQL. The panel includes an API, optional multi-node management and Docker deployment support.

## Install on a Linux server

For a server with root or sudo access, download the installer from this repository:

```bash
curl -fsSL https://raw.githubusercontent.com/sahand-13/silo_vip_network/main/install.sh -o install-silo.sh
sudo bash install-silo.sh
```

The installer downloads the latest stable release for the server architecture, checks the release checksum when provided, and installs the panel as a system service. It can ask for installation settings; keep the generated or selected login details and panel address safe. Allow the panel port and the inbound ports you configure through the server firewall.

To install a specific release, pass its tag:

```bash
sudo bash install-silo.sh v3.11.0
```

To install the rolling development build, use `sudo bash install-silo.sh dev-latest`. Development builds may change between installations. On a noninteractive installation, generated details are written to `/etc/x-ui/install-result.env`; protect this file because it contains credentials.

The published Linux release includes `amd64`, `arm64`, `386`, `armv5`, `armv6`, `armv7` and `s390x` archives. Availability of future assets depends on the release build. If installation fails with a 404, check that the selected [release](https://github.com/sahand-13/silo_vip_network/releases) has the archive for your architecture.

### Service management

```bash
sudo x-ui             # interactive management menu
sudo x-ui status
sudo x-ui restart
sudo systemctl status x-ui
sudo journalctl -u x-ui -n 100 --no-pager
```

The `systemctl` and `journalctl` examples apply to systemd-based systems such as Ubuntu. The management menu provides update and other administration actions. Back up the database and any certificates before changing an existing installation.

## Docker installation

On Linux, install Docker Engine and the Docker Compose plugin, then build this repository's image locally:

```bash
git clone https://github.com/sahand-13/silo_vip_network.git
cd silo_vip_network
sudo bash scripts/silo-server.sh install
```

The helper uses `docker-compose.silo.yml`, Linux host networking and named volumes for the database and certificates. On the first run it prints the command for viewing generated login details. The default panel port for this Docker setup is `2053`.

```bash
sudo bash scripts/silo-server.sh status
sudo bash scripts/silo-server.sh logs
```

After obtaining the desired updated repository code, run `sudo bash scripts/silo-server.sh update` to rebuild and restart the container. Docker and native installations use separate storage; they do not migrate data automatically.

## Development

The backend is Go with CGO and SQLite; the frontend uses React, TypeScript and Vite. Use the Go version declared in `go.mod`, the Node.js version in `.nvmrc`, and a C compiler. See [CONTRIBUTING.md](CONTRIBUTING.md) and [frontend/README.md](frontend/README.md) for setup and platform details.

A typical local setup is:

```bash
cp .env.example .env
cd frontend
npm ci
npm run build
cd ..
go run .
```

The first frontend build is needed because the Go application embeds the frontend output. On first startup, check the terminal for generated credentials. For frontend hot reload, keep the Go panel running and start `npm run dev` in `frontend/`; Vite serves on `http://localhost:5173` and proxies requests to the panel on port `2053`. Xray features also require the appropriate Xray binary and data files in the configured binary directory.

Useful checks:

```bash
cd frontend && npm test && npm run typecheck
# From the repository root:
go test ./...
```

## Release builds

The [release workflow](.github/workflows/release.yml) builds platform archives and checksum files. For a local native release build on Linux with Docker buildx:

```bash
bash scripts/build-native-release.sh amd64
bash scripts/build-native-release.sh arm64
```

Publish each archive together with its `.sha256` file on a release whose tag points to the same source commit. A tag alone is insufficient for the installer; the matching assets must be attached. Cross-architecture builds require a compatible buildx environment.

## License and attribution

This project is licensed under [GNU GPL v3](LICENSE). Silo VIP Network builds upon the open-source [3X-UI](https://github.com/MHSanaei/3x-ui) project and its contributors' work.
