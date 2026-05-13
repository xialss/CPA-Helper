# syntax=docker/dockerfile:1.7

FROM node:20-bookworm-slim AS frontend-build

WORKDIR /app/frontend

COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

COPY VERSION ../VERSION
COPY frontend/ ./
RUN npm run build


FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim AS runtime

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PATH="/app/backend/.venv/bin:$PATH"

WORKDIR /app/backend

COPY backend/.python-version backend/pyproject.toml backend/uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project

COPY backend/ ./
COPY --from=frontend-build /app/frontend/dist /app/frontend/dist

EXPOSE 18317

CMD ["sh", "-c", "uv run --frozen --no-sync alembic upgrade head && uv run --frozen --no-sync -m uvicorn app.main:app --host 0.0.0.0 --port 18317"]
