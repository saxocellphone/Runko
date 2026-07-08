# Web UI image (§28.3 stage 13). Static build served by unprivileged
# nginx (listens 8080, non-root - matches the cluster's PodSecurity
# hygiene). Build context is web/ - src/gen is committed, so no buf or
# proto access is needed at build time.
#
# VITE_RUNKO_URL is a BUILD-time var (Vite inlines it): empty = demo mode
# everywhere (in-memory fake transport, labeled in the sidebar); "/" =
# same-origin live (the path-routed-ingress deployment - release-images
# passes this); an absolute URL = a specific runkod origin. The demo
# scene stays mounted at /demo in every mode. NEVER pass VITE_RUNKO_TOKEN
# to an image build: Vite would inline the deploy token into the public
# bundle - tokens are entered per-browser at runtime (see
# src/api/client.ts).
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
