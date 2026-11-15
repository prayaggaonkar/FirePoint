FROM golang:1.22.2-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/firerouter ./cmd/firerouter \
    && CGO_ENABLED=0 go build -o /out/mockupstream ./cmd/mockupstream \
    && CGO_ENABLED=0 go build -o /out/loadgen ./cmd/loadgen

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/firerouter /usr/local/bin/firerouter
COPY --from=build /out/mockupstream /usr/local/bin/mockupstream
COPY --from=build /out/loadgen /usr/local/bin/loadgen

EXPOSE 8080
USER nobody
ENTRYPOINT ["firerouter"]
