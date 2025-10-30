# build
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY server/go.mod .
RUN go mod download
COPY server/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /server

# final minimal image with netcat for healthcheck
FROM alpine:latest
RUN apk --no-cache add netcat-openbsd
COPY --from=build /server /server
COPY server/cards.json /cards.json
USER 65532:65532
EXPOSE 9000
ENTRYPOINT ["/server"]
