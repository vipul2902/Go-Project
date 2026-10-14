# syntax=docker/dockerfile:1
# ---- build: compile the React app ----
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

# ---- runtime: unprivileged nginx (uid 101, port 8080) ----
FROM nginxinc/nginx-unprivileged:1.30-alpine
COPY nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
ENV API_UPSTREAM=clouddeploy-api:80
EXPOSE 8080
