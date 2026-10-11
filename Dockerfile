FROM golang:1.26.4-bookworm AS dependencies
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download && go mod verify

FROM dependencies AS test
ENV GOMAXPROCS=2 GOFLAGS=-p=2
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=1 go test -race ./... && go vet ./...

FROM dependencies AS build
ENV GOMAXPROCS=2 GOFLAGS=-p=2
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/akasha ./cmd/akasha

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 akasha && useradd -u 10001 -g 10001 --no-create-home akasha \
    && mkdir /data && chown 10001:10001 /data
COPY --from=build /out/akasha /usr/local/bin/akasha
USER 10001:10001
VOLUME /data
EXPOSE 7342
ENTRYPOINT ["/usr/local/bin/akasha"]
CMD ["hub", "--data", "/data", "--listen", ":7342"]
