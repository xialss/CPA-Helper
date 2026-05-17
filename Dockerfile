# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM node:20-bookworm-slim AS frontend-build

WORKDIR /app/frontend

COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,id=cpa-helper-npm,target=/root/.npm,sharing=locked \
    npm ci --prefer-offline

COPY VERSION ../VERSION
COPY frontend/ ./
RUN npm run build


FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS backend-build

ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /app/backend

COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,id=cpa-helper-gomod,target=/go/pkg/mod,sharing=locked \
    go mod download

COPY backend/ ./
RUN --mount=type=cache,id=cpa-helper-gomod,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=cpa-helper-gobuild,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cpa-helper ./cmd/cpa-helper


FROM --platform=$BUILDPLATFORM debian:bookworm-slim AS runtime-assets

RUN --mount=type=cache,id=cpa-helper-apt-cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,id=cpa-helper-apt-lists,target=/var/lib/apt/lists,sharing=locked \
    apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata


FROM debian:bookworm-slim AS runtime

ENV CPA_HELPER_DATA_DIR=/app/data \
    CPA_HELPER_FRONTEND_DIST=/app/frontend/dist

WORKDIR /app

COPY --from=backend-build /out/cpa-helper /app/cpa-helper
COPY --from=frontend-build /app/frontend/dist /app/frontend/dist
COPY --from=runtime-assets /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=runtime-assets /usr/share/zoneinfo /usr/share/zoneinfo

EXPOSE 18317

CMD ["/app/cpa-helper"]
