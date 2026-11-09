# Build with:  podman build -t stash .   (or docker build)
FROM docker.io/library/golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY ui ./ui
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stash . && mkdir -p /out/data

# The final image holds nothing but the program: no shell, no package manager.
FROM scratch
COPY --from=build /out/stash /stash
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV STASH_DATA=/data STASH_LISTEN=:8080
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/stash"]
