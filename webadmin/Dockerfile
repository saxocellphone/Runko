# Deployment admin panel image: static build served by unprivileged
# nginx (listens 8080, non-root), mirroring web/Dockerfile. Build
# context is webadmin/.
#
# VITE_RUNKO_URL is a BUILD-time var (Vite inlines it): "/" = same-origin
# live (the path-routed-ingress deployment - release-images passes
# this); an absolute URL = a specific runkod origin. NEVER pass a
# credential to an image build - operator credentials are entered
# per-browser at runtime (see src/api.ts).
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
COPY --from=build /app/dist /usr/share/nginx/html/admin
EXPOSE 8080
