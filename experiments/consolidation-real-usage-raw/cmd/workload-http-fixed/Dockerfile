FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod main.go .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/workload-http-fixed .

FROM alpine:3.22
RUN adduser -D -u 10001 app
USER 10001:10001
COPY --from=build /out/workload-http-fixed /workload-http-fixed
EXPOSE 8080
ENTRYPOINT ["/workload-http-fixed"]
