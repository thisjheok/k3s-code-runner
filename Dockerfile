FROM golang:1.23-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/code-runner-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/code-runner-worker ./cmd/worker

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=builder /out/code-runner-api /code-runner-api
COPY --from=builder /out/code-runner-worker /code-runner-worker
COPY --from=builder /src/web /web
EXPOSE 8080
ENTRYPOINT ["/code-runner-api"]
