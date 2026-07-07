# Web UI image (§28.3 stage 13). Static build served by unprivileged
# nginx (listens 8080, non-root - matches the cluster's PodSecurity
# hygiene). Build context is web/ - src/gen is committed, so no buf or
# proto access is needed at build time.
#
# VITE_RUNKO_URL is a BUILD-time var (Vite inlines it): leave it empty for
# demo mode (in-memory fake transport, labeled in the sidebar); set it to
# the runkod origin once the Connect server half exists. Same-origin
# deployments (path-routed ingress) will set it to "" plus relative
# routing when that lands.
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
ARG VITE_RUNKO_URL=
ENV VITE_RUNKO_URL=${VITE_RUNKO_URL}
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
