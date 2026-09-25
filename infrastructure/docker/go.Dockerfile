# Builds both Go binaries (api, crawler) into one small image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/crawler ./cmd/crawler

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 atlas
COPY --from=build /out/ /usr/local/bin/
COPY testdata /app/testdata
WORKDIR /app
USER atlas
EXPOSE 8080
ENTRYPOINT ["api"]
