FROM node:20-alpine AS build
WORKDIR /web

# Layer-cache npm deps separately from source
COPY web/package.json web/package-lock.json* ./
RUN npm ci --prefer-offline

COPY web/ .
RUN npm run build
# Produces /web/dist/{index.html,assets/}

FROM nginx:1.27-alpine

# React app at /ui/ (matches vite base: '/ui/')
COPY --from=build /web/dist /usr/share/nginx/html/ui

# Custom config: SPA fallback + gateway proxy
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf

# Remove the default nginx index page
RUN rm -f /usr/share/nginx/html/index.html

EXPOSE 80
