FROM golang:1.23 AS builder

WORKDIR /go/src/migrator
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
ENV GOCACHE=/root/.cache/go-build
RUN --mount=type=cache,target="/root/.cache/go-build" CGO_ENABLED=0 go build -o /migrator -ldflags="-w -s" .

FROM gcr.io/distroless/static-debian12

COPY --from=builder /migrator /migrator

VOLUME ["/migrations"]
VOLUME ["/templates"]
EXPOSE 8080

ENTRYPOINT ["/migrator"]
