#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
(
  cd web
  npm ci
  npm run check
  npm run lint
  npm run test:unit
  npm run build
)
go test ./...
go vet ./...
go build -o bin/http-wrapper ./cmd/http-wrapper
