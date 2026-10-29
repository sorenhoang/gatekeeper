FROM golang:1.23-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod ./
COPY internal ./internal
COPY cmd ./cmd
RUN test -n "$SERVICE" || (echo "build arg SERVICE is required" && exit 1)
RUN CGO_ENABLED=0 go build -trimpath -o /out/app ./cmd/$SERVICE

# No shell or curl in the runtime image: debug the services from the gateway container.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
