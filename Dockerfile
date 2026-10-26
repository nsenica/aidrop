FROM golang:1.26-alpine AS build

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/aidrop ./cmd/aidrop

FROM alpine:3.22

RUN addgroup -S -g 10001 aidrop \
	&& adduser -S -D -H -u 10001 -G aidrop aidrop \
	&& mkdir -p /data \
	&& chown 10001:10001 /data

COPY --from=build /out/aidrop /usr/local/bin/aidrop

ENV AIDROP_DATA_DIR=/data \
	AIDROP_LISTEN_ADDR=0.0.0.0:6420

USER 10001:10001
EXPOSE 6420
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD ["/bin/sh", "-c", "wget -q -O /dev/null http://127.0.0.1:6420/healthz"]

ENTRYPOINT ["/usr/local/bin/aidrop"]
