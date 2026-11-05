# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go build -o /out/business-api .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/business-api /usr/local/bin/business-api
EXPOSE 8080
CMD ["business-api"]
