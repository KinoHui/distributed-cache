FROM golang:1.23-alpine AS builder

ENV GOPROXY=https://goproxy.cn,direct 

WORKDIR /app

COPY . .

RUN go mod download

RUN go build -o distributed-cache ./main.go

FROM alpine:3.19

WORKDIR /app
COPY --from=builder /app/distributed-cache .
COPY --from=builder /app/etc .
EXPOSE 8001 8002 8003 9999
# CMD ["./distributed-cache"]