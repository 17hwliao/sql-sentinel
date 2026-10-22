FROM node:22.22.2-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY index.html vite.config.js product.json ./
COPY src ./src
RUN npm run build

FROM node:22.22.2-alpine AS runtime
ENV NODE_ENV=production HOST=0.0.0.0
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-fund --no-audit && npm cache clean --force
COPY --from=build /app/dist ./dist
COPY server.mjs product.json check-config.mjs ./
USER node
EXPOSE 4181
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD node -e "fetch('http://127.0.0.1:4181/healthz').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))"
CMD ["node", "server.mjs"]
