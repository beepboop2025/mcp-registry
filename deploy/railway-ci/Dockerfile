FROM golang:1.26-bookworm AS verification
ENV GOMAXPROCS=2
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN timeout --signal=TERM --kill-after=15 600 go test -count=1 ./... \
    && go build -o /out/validate ./cmd/validate \
    && go build -o /out/build ./cmd/build \
    && go build -o /out/catalog ./cmd/catalog \
    && go build -o /out/clean ./cmd/clean

FROM debian:bookworm-slim
COPY --from=verification /out /verified-tools
USER 65532:65532
CMD ["sh", "-c", "printf 'RAILWAY_REGISTRY_CORE_CI_PASS source=%s deployment=%s\\n' \"${RAILWAY_GIT_COMMIT_SHA:-unavailable}\" \"${RAILWAY_DEPLOYMENT_ID:-unavailable}\""]
