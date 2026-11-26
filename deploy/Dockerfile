# Static FirePoint binaries. The builder is the official Go image.
# The final image is distroless static: CA certificates, no shell, no
# package manager. The nonroot tag runs as uid 65532.

ARG GO_VERSION=1.22.2

FROM golang:${GO_VERSION}-bookworm AS build

WORKDIR /src

# VERSION is stamped into each binary. Declare it in this stage so the
# build arg is not dropped at the FROM boundary.
ARG VERSION=dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off keeps the binaries static, which is what distroless static can run.
# -trimpath removes local paths. -s -w drops the symbol table and debug info.
RUN set -eu; \
    for bin in firepoint mockupstream loadgen; do \
      CGO_ENABLED=0 go build -trimpath -buildvcs=false \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o "/out/${bin}" "./cmd/${bin}"; \
    done

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev

LABEL org.opencontainers.image.title="firepoint" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /out/firepoint /usr/local/bin/firepoint
COPY --from=build /out/mockupstream /usr/local/bin/mockupstream
COPY --from=build /out/loadgen /usr/local/bin/loadgen

USER nonroot

EXPOSE 8080

ENTRYPOINT ["firepoint"]
