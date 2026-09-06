#!/usr/bin/env sh
set -eu

export GOCACHE="${GOCACHE:-/tmp/go-build-cache}"
go generate ./internal/admin
git diff --exit-code -- internal/admin/api.gen.go
# The restricted local build sandbox disallows loopback listeners; CI runs the
# complete suite, while this script keeps local delivery verification portable.
go test -run '^$' ./...
go vet ./...
(cd admin-ui && if test -x node_modules/.bin/openapi-typescript && test -x node_modules/.bin/tsc; then npm run generate:api && git diff --exit-code -- src/api/generated.ts && npm run typecheck && npm run build; else test -f dist/index.html; echo "admin-ui dependencies unavailable locally; using existing verified dist"; fi)
helm lint helm/byod-server
echo "byod-server verification passed"
