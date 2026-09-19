FROM node:22-bookworm-slim AS admin-ui
WORKDIR /src/admin-ui
COPY admin-ui/package*.json ./
RUN npm ci
COPY admin-ui/ ./
COPY openapi.yaml ../openapi.yaml
COPY exam-ui/ ../exam-ui/
RUN npm run generate:api && npm run build
RUN mkdir -p ../exam-ui/dist && ./node_modules/.bin/tsc ../exam-ui/app.ts \
  --target ES2022 --module ES2022 --lib DOM,ES2022 --skipLibCheck \
  --outDir ../exam-ui/dist
RUN cp ../exam-ui/index.html ../exam-ui/dist/index.html && \
  cp ../exam-ui/style.css ../exam-ui/dist/style.css && \
  cp ../exam-ui/bridge.html ../exam-ui/dist/bridge.html

FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=admin-ui /src/admin-ui/dist ./admin-ui/dist
COPY --from=admin-ui /src/exam-ui/dist ./exam-ui/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/byod-server ./cmd/byod-server
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/byod-server /byod-server
EXPOSE 8787
# Kubernetes runAsNonRoot admission requires a numeric UID when the image
# metadata uses a named user. Distroless' nonroot account is UID/GID 65532.
USER 65532:65532
ENTRYPOINT ["/byod-server"]
