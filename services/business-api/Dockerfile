# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go build -o /out/business-api .

# CLI operator (F1-OPERATOR-ENTITLEMENT-IMPLEMENT): target opsional;
# target default (tahap akhir file ini) tetap business-api.
FROM build AS cli-build
RUN go build -o /out/grant-entitlement ./cmd/grant-entitlement

FROM debian:bookworm-slim AS cli
COPY --from=cli-build /out/grant-entitlement /usr/local/bin/grant-entitlement
ENTRYPOINT ["grant-entitlement"]

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl \
  && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/business-api /usr/local/bin/business-api
EXPOSE 8080
CMD ["business-api"]
