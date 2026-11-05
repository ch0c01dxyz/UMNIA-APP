# syntax=docker/dockerfile:1
FROM node:22-bookworm AS build
WORKDIR /app
COPY package.json package-lock.json ./
# Match CI (.github/workflows/ci.yml): @nuxt/cli peer ranges for cac/commander
# conflict with the Nuxt tree, so strict npm ci fails (EUSAGE) on a fresh
# lockfile. --legacy-peer-deps only relaxes peer validation; the install
# is still clean from the lockfile.
RUN npm ci --legacy-peer-deps
COPY . .
RUN npm run build

FROM node:22-bookworm
WORKDIR /app
COPY --from=build /app/.output ./.output
EXPOSE 3000
ENV NITRO_HOST=0.0.0.0
ENV NITRO_PORT=3000
CMD ["node", ".output/server/index.mjs"]
