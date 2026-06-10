FROM golang:1.24-alpine AS build
WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/workload-http main.go

FROM alpine:3.22
RUN adduser -D -u 10001 app
USER 10001:10001
COPY --from=build /out/workload-http /workload-http
EXPOSE 8080
ENTRYPOINT ["/workload-http"]
