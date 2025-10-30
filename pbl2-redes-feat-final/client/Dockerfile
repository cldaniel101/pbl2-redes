# build
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY client/go.mod .
RUN go mod download
COPY client/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /client

# final minimal image
FROM scratch
COPY --from=build /client /client
USER 65532:65532
ENTRYPOINT ["/client"]
