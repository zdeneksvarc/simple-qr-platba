FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/simple-qr-platba .

FROM alpine:3.22

RUN adduser -D -H -u 10001 app \
	&& apk add --no-cache ca-certificates

COPY --from=build /out/simple-qr-platba /usr/local/bin/simple-qr-platba

ENV HTTP_PORT=3000
EXPOSE 3000

USER app

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD wget -qO- "http://127.0.0.1:${HTTP_PORT}/healthz" >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/simple-qr-platba"]
