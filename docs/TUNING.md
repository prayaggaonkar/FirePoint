# Tuning

The binary does not set a file-descriptor limit, an ephemeral port range, or `GOMAXPROCS`. Those belong to the host or the container. The image is distroless and runs as uid 65532. It inherits whatever the runtime gives it.

## File descriptors

A streaming response holds the client socket until the last byte or the client hangs up. That is one descriptor per in-flight stream on the accept side.

Toward the upstream, the process uses one shared transport. It tries HTTP/2, keeps up to 64 idle connections per host, and drops an idle connection after 90 seconds. Many streams then share a few upstream sockets. If the peer does not speak HTTP/2, each in-flight request is its own connection, so count about two descriptors per stream (client plus upstream) plus the listening socket and the idle pool.

A common default limit is 1024 (`ulimit -n`). A few hundred long-lived streams can pass that, and the next `accept` or dial fails. Raise the limit for the service, not inside the binary:

```bash
ulimit -n
```

Docker:

```bash
docker run --ulimit nofile=65535:65535 ...
```

On a systemd host, `LimitNOFILE=` on the unit. In Kubernetes the limit comes from the node and the pod security context. Check it from a sidecar or from the host. The distroless image has no shell.

## Ephemeral ports

Each new outbound TCP connection takes a local port from `net.ipv4.ip_local_port_range`. The usual range is 32768–60999, about 28,000 ports. After a connection closes it can sit in `TIME_WAIT` until `net.ipv4.tcp_fin_timeout` (often 60 seconds), and that port cannot be reused yet.

HTTP/2 to one upstream host avoids a port per stream, which is why the transport attempts it. A burst of HTTP/1 dials, or a different connection per request, spends the range and dials fail with `cannot assign requested address`.

See the current range:

```bash
sysctl net.ipv4.ip_local_port_range
```

Widening it, for example to `1024 65535`, is a host change. Keep it clear of ports the proxy and the mock bind (8080, 9000, and anything else on that host). Loopback counts too: loadgen, the proxy, and the mock on one machine still take ephemeral ports for each connection.

## GOMAXPROCS in containers

This module is Go 1.22.2. That runtime sets `GOMAXPROCS` from the number of logical CPUs it can see, not from a cgroup CPU quota. A container limited to 1 CPU on a large node still starts one thread per visible CPU. The extra threads compete with the quota and add scheduler delay on the stream path.

Set the variable to the number of CPUs the container may actually run. A limit of 2:

```bash
docker run -e GOMAXPROCS=2 ...
```

The same variable works in Compose and in a Kubernetes pod spec. A cpuset (`--cpuset-cpus`) also shrinks what Go observes. A fractional CPU limit such as `--cpus=0.5` does not, on this Go version. The process does not read the cgroup and does not import a library that does.
