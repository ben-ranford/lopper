# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine AS build
WORKDIR /src

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk add --no-cache build-base

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	ldflags="-s -w" \
	&& ldflags="${ldflags} -X github.com/ben-ranford/lopper/internal/version.version=${VERSION}" \
	&& ldflags="${ldflags} -X github.com/ben-ranford/lopper/internal/version.commit=${GIT_COMMIT}" \
	&& ldflags="${ldflags} -X github.com/ben-ranford/lopper/internal/version.buildDate=${BUILD_DATE}" \
	&& CGO_ENABLED=1 go build -trimpath -ldflags="${ldflags}" -o /out/lopper ./cmd/lopper

FROM alpine:3.23
RUN apk add --no-cache libstdc++ ca-certificates \
	&& addgroup -S lopper \
	&& adduser -S -G lopper lopper
COPY --from=build /out/lopper /usr/local/bin/lopper
USER lopper
ENTRYPOINT ["/usr/local/bin/lopper"]
