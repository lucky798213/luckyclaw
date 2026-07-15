FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/luckyclaw \
    ./cmd/luckyclaw \
    && mkdir -p /out/data

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

WORKDIR /app

COPY --from=build --chown=65532:65532 /out/luckyclaw /app/luckyclaw
COPY --chown=65532:65532 config.example.yaml /app/config.yaml
COPY --chown=65532:65532 SOUL.md /app/SOUL.md
COPY --from=build --chown=65532:65532 /out/data /app/data

USER 65532:65532
VOLUME ["/app/data"]

ENTRYPOINT ["/app/luckyclaw"]
