# CPA Helper

English | [中文](README.md)

[![FastAPI](https://img.shields.io/badge/FastAPI-0.115+-009688?logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)
[![Vue](https://img.shields.io/badge/Vue-3.5+-42b883?logo=vuedotjs&logoColor=white)](https://vuejs.org/)
[![Vite](https://img.shields.io/badge/Vite-5.4+-646cff?logo=vite&logoColor=white)](https://vitejs.dev/)
[![Python](https://img.shields.io/badge/Python-3.12+-3776ab?logo=python&logoColor=white)](https://www.python.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

CPA Helper is a local self-hosted multi-user administration panel for CLIProxyAPI / CPA users. It centralizes usage analytics, request records, user accounts, API keys, model pricing, available models and Codex auth file inspection.

It separates API keys and usage data by user: each user can create and manage their own keys and inspect their own requests, tokens and cost statistics, while administrators can create or disable regular accounts and review global plus per-user usage. It is built with FastAPI, SQLite, Vue 3 and Vite, with runtime data stored in the root-level `data/` directory by default.

## Table of Contents

- [Features](#features)
- [Screenshots](#screenshots)
- [Tech Stack](#tech-stack)
- [Project Structure](#project-structure)
- [Requirements](#requirements)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Development and Checks](#development-and-checks)
- [Contributing](#contributing)
- [License](#license)

## Features

- **Usage analytics and cost estimation**: Track requests, tokens, success rate, latency, model distribution and estimated cost from global, per-user and current-account views.
- **Request record tracing**: Admins can filter global request events by time, user, API key description, provider, model, endpoint and failure state; regular users inspect only their own account records.
- **User and permission management**: Provide administrator and regular-user views; admins can create or disable regular accounts and manage nicknames, login accounts, passwords and roles.
- **API key lifecycle management**: Each user can independently create, edit, copy and delete their own API keys and synchronize them to CPA, with usage counted and displayed per user.
- **Model pricing maintenance**: Maintain input, output, cached and reasoning prices in USD per million tokens; historical costs are recalculated with current prices.
- **Available model aggregation**: Query available models through the current account's bound CPA API keys and enrich them with local pricing data.
- **CLIProxyAPI / CPAMC integration**: Configure the service URL, management key, usage queue and local collector options to persist remote usage events into SQLite.
- **Codex auth file inspection**: Support Cron scheduling, quota thresholds, dry-run mode, concurrent workers, priority rules, account enable/disable and deletion.
- **Local-first data storage**: Use SQLite and the `data/` directory by default, with `CPA_HELPER_DATA_DIR` available for overriding the runtime data path.
- **Modern admin interface**: Built with Vue 3, Naive UI, ECharts and lucide icons, with light, dark and system theme modes.

## Screenshots

### Admin

**Usage dashboard**

Admins can inspect global request volume, tokens, cost, trends and distributions by time range, user, model and endpoint.

![Usage dashboard](pictures/历史用量.png)

**Request records**

Admins can filter global request events, while regular users can inspect records scoped to their own account, with drawer-based detail views.

![Request records](pictures/请求明细.png)

**User management**

Admins can create or disable regular accounts, manage nicknames, roles and enabled status, and review per-user daily usage summaries.

![User management](pictures/用户管理.png)

**Model pricing**

Maintain model pricing and recalculate historical request costs using the latest configured prices.

![Model pricing](pictures/模型价格.png)

**System settings**

Configure the CLIProxyAPI / CPAMC endpoint, management key, local collector, polling options and theme preference.

![System settings](pictures/系统设置.png)

### Account Inspection

**Inspection settings**

Configure Codex auth file inspection with Cron schedules, quota thresholds, timeouts, retries, worker count and priority rules.

![Inspection settings](pictures/巡检设置.png)

**Account status**

Review auth file health, quota windows, account types, priorities and the latest inspection actions.

![Account status](pictures/账号状态.png)

### Account Views

**My usage**

Each user can review their own requests, tokens, costs, trends and model usage.

![My usage](pictures/我的账户.png)

**My records**

Each user can inspect request events and details scoped to their own account, separated from other users.

![My records](pictures/我的明细.png)

**API keys**

Each account can independently create and manage its own API keys and review daily request, token and cost summaries.

![API keys](pictures/API密钥.png)

**Available models**

Query available models through bound CPA API keys and display source keys with pricing information.

![Available models](pictures/可用模型.png)

**Account settings**

View the current signed-in account and update the password.

![Account settings](pictures/账户设置.png)

## Tech Stack

- **Backend**: FastAPI, SQLModel, SQLite, Alembic, httpx, croniter and uvicorn.
- **Frontend**: Vue 3, Vite, TypeScript, Vue Router, Naive UI, ECharts and lucide-vue-next.
- **Runtime data**: Stored in root-level `data/` by default; the SQLite database is `data/db/cpa_helper.sqlite3`.
- **API shape**: The backend exposes `/api/*`; the Vite development server proxies API calls to `http://127.0.0.1:18317`.

## Project Structure

```text
CPA-Helper/
├── backend/                 # FastAPI backend project
│   ├── app/                 # APIs, services, models, configuration and database access
│   ├── alembic/             # Alembic migrations
│   ├── tests/               # Backend tests
│   └── pyproject.toml       # Python project and dependency configuration
├── frontend/                # Vue + Vite frontend project
│   ├── src/                 # App, feature modules, shared utilities and styles
│   ├── public/              # Static assets
│   └── package.json         # Frontend dependencies and scripts
├── pictures/                # README screenshots
├── docs/                    # Reference documentation
├── data/                    # Runtime data, ignored by Git by default
├── README.md
├── README.en.md
└── LICENSE
```

## Requirements

- Python 3.12 or newer.
- [uv](https://docs.astral.sh/uv/) for synchronizing the backend project environment.
- Node.js 20 or newer.
- npm.
- An accessible CLIProxyAPI / CPA service. The default URL is `http://127.0.0.1:8317`.

## Quick Start

### 1. Docker Compose deployment (recommended)

Create `docker-compose.yml` in the deployment directory:

```yaml
services:
  cpa-helper:
    image: walkingd/cpa-helper:latest
    container_name: cpa-helper
    restart: always
    network_mode: host
    environment:
      - TZ=Asia/Shanghai
    ports:
      - "18317:18317"
    volumes:
      - ./data:/app/data
```

Then pull the image and start the service:

```powershell
docker compose pull
docker compose up -d
```

Open:

```text
http://127.0.0.1:18317
```

On first visit, the application guides you through creating the first administrator account.

### 2. Clone the repository

```powershell
git clone <your-repo-url>
cd CPA-Helper
```

### 3. Start the backend

Run all backend commands from `backend/`:

```powershell
cd backend
uv sync
uv run alembic upgrade head
uv run -m uvicorn app.main:app --host 0.0.0.0 --port 18317
```

Health check:

```powershell
curl http://127.0.0.1:18317/api/health
```

Expected response:

```json
{"status":"ok"}
```

### 4. Start the frontend development server

Open a new terminal and run from `frontend/`:

```powershell
cd frontend
npm install
npm run dev
```

Open:

```text
http://127.0.0.1:5173
```

On first visit, the application guides you through creating the first administrator account.

### 5. Single-service preview or deployment

To let FastAPI serve the frontend static files, build the frontend first:

```powershell
cd frontend
npm install
npm run build
```

Then start the backend:

```powershell
cd ../backend
uv sync
uv run alembic upgrade head
uv run -m uvicorn app.main:app --host 0.0.0.0 --port 18317
```

Open:

```text
http://127.0.0.1:18317
```

When `frontend/dist` exists, the backend serves the built single-page application.

## Configuration

### CLIProxyAPI / CPAMC

Use the System Settings page to configure:

- **CLIProxyAPI / CPAMC URL**: defaults to `http://127.0.0.1:8317`.
- **Management key**: used to access the CLIProxyAPI Management API.
- **Enable local collector**: when enabled, CPA Helper reads events from the usage queue and writes them to the local database.
- **Batch size, polling interval and retry interval**: control local collector throughput and failure retry behavior.

### Data Directory

Default runtime data directory:

```text
data/
```

Default SQLite database:

```text
data/db/cpa_helper.sqlite3
```

Override the runtime data directory with:

```powershell
$env:CPA_HELPER_DATA_DIR="<your-data-dir>"
```

Then start the backend service.

### Account Inspection

The Inspection Settings page manages Codex auth files:

- Cron expressions define the automatic inspection schedule.
- Quota thresholds decide when account priority should be degraded or restored.
- Dry-run mode records planned actions without applying destructive changes.
- Priority rules define default scheduling weights by account type.
- The Account Status page shows health, quota, latest inspection, enabled state and manual priority.

## Development and Checks

Backend:

```powershell
cd backend
uv run ruff check .
uv run -m pytest
```

Frontend:

```powershell
cd frontend
npm run lint
npm run build
```

Database migrations:

```powershell
cd backend
uv run alembic current
uv run alembic upgrade head
```

## Contributing

Issues and pull requests are welcome. Before submitting changes, please check:

- Backend passes `uv run ruff check .` and `uv run -m pytest`.
- Frontend passes `npm run lint` and `npm run build`.
- Relational schema changes include Alembic migration files.
- Local runtime data, virtual environments, build outputs and secrets are not committed.

## License

This project is open-sourced under the [MIT License](LICENSE).
