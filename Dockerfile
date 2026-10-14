# Switchyard server — analytics, overview and phone pushes for all your Claude accounts.
# Zero npm dependencies: the image is just Node 22 plus the server, core and web folders.
FROM node:22-alpine
ENV NODE_ENV=production PORT=8787 SWITCHYARD_DATA=/data NODE_NO_WARNINGS=1
WORKDIR /app
COPY package.json ./
COPY core ./core
COPY server ./server
COPY web ./web
COPY assets/icon.png ./assets/icon.png
RUN mkdir -p /data && chown node:node /data
USER node
VOLUME /data
EXPOSE 8787
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD wget -qO- http://127.0.0.1:8787/healthz >/dev/null || exit 1
CMD ["node", "server/server.js"]
