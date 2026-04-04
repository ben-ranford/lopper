# syntax=docker/dockerfile:1.23
FROM golang:1.26-alpine AS build
WORKDIR /src

ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETPLATFORM

RUN apk add --no-cache build-base

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,id=go-mod-cache,sharing=locked go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod,id=go-mod-cache,sharing=locked \
	--mount=type=cache,target=/root/.cache/go-build,id=go-build-cache-${TARGETPLATFORM},sharing=locked \
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
