FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go install github.com/bufbuild/buf/cmd/buf@v1.50.0 \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1 \
    && buf dep update \
    && buf generate \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/medicion ./services/medicion/cmd/medicion

FROM alpine:3.20
RUN addgroup -S app && adduser -S -G app app
COPY --from=build /out/medicion /usr/local/bin/medicion
COPY --from=build /src/deploy/migrations/003_measurements.sql /app/migrations/003_measurements.sql
USER app
EXPOSE 9090 9100
ENTRYPOINT ["/usr/local/bin/medicion"]
