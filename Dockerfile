# build with the vendored deps, then ship the static binary on alpine: small, but keeps a shell
# so you can `docker exec -it embyfin-mcp sh` to poke at things. runs as a non-root user.
# make docker passes VERSION/COMMIT from git; a bare `docker build .` reports "dev".
#
# REGISTRY is where the two base images come from: Docker Hub, unless a build says otherwise. CI
# builds again from Google's mirror of it (mirror.gcr.io/library) when Docker Hub turns its
# runner away. Nothing here needs a newer Dockerfile syntax than every builder has, so no syntax
# image is named: that was a third thing to fetch from Docker Hub before the build could start.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24
ARG REGISTRY=docker.io/library

FROM ${REGISTRY}/golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor \
      -ldflags "-s -w \
        -X github.com/katbyte/go-kt/version.Version=${VERSION} \
        -X github.com/katbyte/go-kt/version.GitCommit=${COMMIT}" \
      -o /embyfin-mcp .

FROM ${REGISTRY}/alpine:${ALPINE_VERSION}
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 65532 embyfin
COPY --from=build /embyfin-mcp /usr/local/bin/embyfin-mcp
USER embyfin
# HTTP transport by default in the container; set EMBYFIN_SERVER and EMBYFIN_TOKEN at runtime,
# EMBYFIN_AUTH_TOKEN to protect the endpoint. the healthcheck assumes the default port.
ENV EMBYFIN_LISTEN=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["embyfin-mcp"]
CMD ["serve"]
