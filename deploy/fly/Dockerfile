# Build context is the repository root, not this directory.
#   fly deploy . --config deploy/fly/fly.toml --dockerfile deploy/fly/Dockerfile
ARG GO_VERSION=1.22.2
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
ARG VERSION=dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN set -eu; \
    for bin in firepoint mockupstream sandbox; do \
      CGO_ENABLED=0 go build -trimpath -buildvcs=false \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o "/out/${bin}" "./cmd/${bin}"; \
    done

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/firepoint /usr/local/bin/firepoint
COPY --from=build /out/mockupstream /usr/local/bin/mockupstream
COPY --from=build /out/sandbox /usr/local/bin/sandbox
COPY deploy/fly/router.json /config/router.json
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sandbox"]
