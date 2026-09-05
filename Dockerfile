FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/byod-middleware ./cmd/byod-middleware
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/byod-middleware /byod-middleware
EXPOSE 8787
USER nonroot:nonroot
ENTRYPOINT ["/byod-middleware"]
