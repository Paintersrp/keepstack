# syntax=docker/dockerfile:1

FROM node:20-bookworm AS build
WORKDIR /app

COPY apps/web/package.json apps/web/package-lock.json ./
RUN npm ci

COPY apps/web/tsconfig.json ./
COPY apps/web/tsconfig.node.json ./
COPY apps/web/vite.config.ts ./
COPY apps/web/tailwind.config.ts ./
COPY apps/web/postcss.config.cjs ./
COPY apps/web/vitest.config.ts ./
COPY apps/web/index.html ./
COPY apps/web/src ./src

RUN npm run build

FROM nginx:1.25-alpine
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
