# Build
FROM golang:1.18 as build

WORKDIR /app

COPY go.mod go.sum main.go ./

RUN go mod download

RUN CGO_ENABLED=0 go build -o scaledobject-creator

# Final
FROM gcr.io/distroless/static AS final

LABEL maintainer="alfianabdi"

USER nonroot:nonroot

WORKDIR /app

COPY --from=build --chown=nonroot:nonroot /app/scaledobject-creator /scaledobject-creator

COPY --chown=nonroot:nonroot config /home/nonroot/.kube/config

ENTRYPOINT [ "/scaledobject-creator" ]
