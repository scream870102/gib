# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gib .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates \
	&& adduser -D -H -s /sbin/nologin app

COPY --from=build /out/gib /usr/local/bin/gib

USER app
ENTRYPOINT ["gib"]
