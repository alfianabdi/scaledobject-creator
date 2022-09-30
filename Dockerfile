# Build
FROM golang:1.18 as build

COPY go.mod go.sum ./

RUN go mod download

RUN go build -o /scaledobject-creator

# Final
FROM gcr.io/distroless/static AS final

LABEL maintainer="alfianabdi"

USER nonroot:nonroot

COPY --from=build --chown=nonroot:nonroot /scaledobject-creator /scaledobject-creator

ENTRYPOINT [ "/scaledobject-creator" ]