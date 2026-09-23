# syntax=docker/dockerfile:1

# lotsman as a container: acceptance criterion 11.
#
# The image contains the binary and nothing else -- no shell, no package
# manager, no libc. That is not minimalism for its own sake. This process
# holds credentials and talks to an API on an agent's behalf, so the cost of
# anything else being in the image is that it is available to whoever gets in.

# The builder tracks the Go the workflows build with (GO_VERSION in
# .github/workflows/ci.yaml), not the go.mod floor: the floor is the oldest
# release this code compiles with, and building on an end-of-life line means
# shipping a standard library that has stopped taking security fixes.
FROM golang:1.27 AS build

WORKDIR /src

# Dependencies first, so that editing source does not re-download the module
# cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# CGO off and -trimpath: a static binary needs no base image to match, and
# build paths are not the operator's business (they also make two machines
# produce different bytes for the same source).
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w \
      -X github.com/razrabotchik/lotsman/internal/buildinfo.version=${VERSION} \
      -X github.com/razrabotchik/lotsman/internal/buildinfo.commit=${COMMIT} \
      -X github.com/razrabotchik/lotsman/internal/buildinfo.date=${DATE}" \
    -o /out/lotsman ./cmd/lotsman

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/lotsman /usr/local/bin/lotsman

# Non-root by identity, not by convention: the image declares the user, so an
# operator who forgets `--user` still does not run this as root.
USER nonroot:nonroot

# Nothing is written at run time, so the root filesystem can be read-only.
# `docker run --read-only` is part of the test that proves it.

# The documented default bind is loopback (FR-70), which inside a container
# means nothing outside it can connect. Reaching a containerised endpoint is
# therefore a deliberate --listen 0.0.0.0:8080, and that in turn needs inbound
# authentication configured -- the container boundary is not a boundary
# lotsman can see.
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/lotsman"]
CMD ["help"]
