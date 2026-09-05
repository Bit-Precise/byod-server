FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/byod-middleware ./cmd/byod-middleware
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/byod-middleware /byod-middleware
EXPOSE 8787
# Kubernetes runAsNonRoot admission requires a numeric UID when the image
# metadata uses a named user. Distroless' nonroot account is UID/GID 65532.
USER 65532:65532
ENTRYPOINT ["/byod-middleware"]
