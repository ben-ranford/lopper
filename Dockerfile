FROM golang:1.26-alpine AS build
WORKDIR /src

RUN apk add --no-cache build-base

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/lopper ./cmd/lopper

FROM alpine:3.23
RUN apk add --no-cache libstdc++ ca-certificates \
	&& addgroup -S lopper \
	&& adduser -S -G lopper lopper
COPY --from=build /out/lopper /usr/local/bin/lopper
USER lopper
ENTRYPOINT ["/usr/local/bin/lopper"]
