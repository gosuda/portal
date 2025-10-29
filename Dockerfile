FROM golang:1 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
# Install make to use Makefile and build the server
RUN apt-get update && apt-get install -y --no-install-recommends make && rm -rf /var/lib/apt/lists/*

RUN --mount=type=cache,target=/go/pkg/mod \
    make build-server && install -D bin/relayserver /out/relayserver

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/relayserver /usr/bin/relayserver

EXPOSE 4017

ENTRYPOINT ["/usr/bin/relayserver"]
