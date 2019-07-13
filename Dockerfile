# Builder stage
FROM golang:1.12 as builder

COPY go.mod go.sum /app/
WORKDIR /app
RUN go mod download

COPY . ./

RUN go test ./...
RUN go vet ./...

ENV GOOS=linux GOARCH=amd64 CGO_ENABLED=0
RUN go build -ldflags '-s' -o /go/bin/api ./cmd/api

# Final image stage
FROM alpine:latest

RUN apk add --update ca-certificates && rm -rf /var/cache/apk/*
WORKDIR app

COPY --from=builder /go/bin/api ./

RUN chmod +x api

CMD ["./api"]

EXPOSE 8282
EXPOSE 9000
