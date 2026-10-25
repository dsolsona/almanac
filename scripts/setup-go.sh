#!/bin/sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
export GOTOOLCHAIN=go1.27.3
export GOWORK=off
export GOMODCACHE="$project_root/.local/go/mod"
export GOCACHE="$project_root/.local/go/build"
guidelines_dir="$project_root/.local/cache/go-modern-guidelines/v0.1.1"
mkdir -p "$project_root/.local/bin" "$guidelines_dir"

# Go verifies and caches its pinned toolchain through its module mechanism.
go version
toolchain_root=$(go env GOROOT)
ln -sfn "$toolchain_root/bin/go" "$project_root/.local/bin/go"
ln -sfn "$toolchain_root/bin/gofmt" "$project_root/.local/bin/gofmt"
GOBIN="$guidelines_dir" go install github.com/JetBrains/go-modern-guidelines@v0.1.1
ln -sfn "$guidelines_dir/go-modern-guidelines" "$project_root/.local/bin/go-modern-guidelines"
"$project_root/.local/bin/go-modern-guidelines" --version
GOBIN="$project_root/.local/bin" go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
"$project_root/.local/bin/oapi-codegen" --version
GOPROXY="${GOPROXY:-https://proxy.golang.org}" "$project_root/.local/bin/go" mod download
