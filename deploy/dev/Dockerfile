# Development image: every toolchain the repository needs to build, generate and verify.
# It is never published. Base images are pinned by their multi-architecture index digest;
# scripts/check-image-pins.py keeps every Dockerfile and Compose file on the same digest.

FROM victoriametrics/victoria-metrics:v1.155.0@sha256:f60dc268b54ecf87f6e09d740c2b732838aa9193090cc741417c9f6386a130df AS victoria-metrics

FROM node:26.8.1-bookworm-slim@sha256:367679cf9792759492a486e4aa4b421764d71a9546a6dae8aab81a99eb797b3e AS node

FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9

# Pinned tool versions; the Go toolchain and Node come from the base images above.
ARG PLAYWRIGHT_VERSION=1.64.1
ARG GO_MODERN_GUIDELINES_VERSION=v0.1.1
ARG OAPI_CODEGEN_VERSION=v2.8.0
ARG SQLC_VERSION=v1.31.1

ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOMODCACHE=/cache/go/mod \
    GOCACHE=/cache/go/build \
    npm_config_cache=/cache/npm

COPY --from=node /usr/local /usr/local
COPY --from=victoria-metrics /victoria-metrics-prod /usr/local/bin/victoria-metrics-prod

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl gcc git libc6-dev make postgresql-client procps python3 ripgrep \
    && rm -rf /var/lib/apt/lists/*

# Chromium for the Playwright matrix, installed once for the pinned Playwright release.
RUN npx --yes "playwright@${PLAYWRIGHT_VERSION}" install --with-deps chromium \
    && chmod -R a+rX /opt/ms-playwright \
    && rm -rf /root/.npm

# Generators and the Go guideline tool, versioned like the host setup used to be.
RUN GOPROXY=https://proxy.golang.org GOBIN=/usr/local/bin GOMODCACHE=/tmp/gomod GOCACHE=/tmp/gocache \
        go install "github.com/JetBrains/go-modern-guidelines@${GO_MODERN_GUIDELINES_VERSION}" \
    && GOPROXY=https://proxy.golang.org GOBIN=/usr/local/bin GOMODCACHE=/tmp/gomod GOCACHE=/tmp/gocache \
        go install "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@${OAPI_CODEGEN_VERSION}" \
    && GOPROXY=https://proxy.golang.org GOBIN=/usr/local/bin GOMODCACHE=/tmp/gomod GOCACHE=/tmp/gocache \
        go install "github.com/sqlc-dev/sqlc/cmd/sqlc@${SQLC_VERSION}" \
    && rm -rf /tmp/gomod /tmp/gocache

# The checkout is mounted at /workspace and caches at /cache (named volumes), owned by a user whose
# numeric identity matches the host user so files written into the checkout stay editable.
ARG USER_ID=1000
ARG GROUP_ID=1000
RUN groupadd --gid "${GROUP_ID}" dev \
    && useradd --uid "${USER_ID}" --gid "${GROUP_ID}" --create-home --shell /bin/bash dev \
    && mkdir -p /workspace /cache/go/mod /cache/go/build /cache/npm \
    && chown -R dev:dev /workspace /cache
USER dev
WORKDIR /workspace
CMD ["/bin/bash"]
