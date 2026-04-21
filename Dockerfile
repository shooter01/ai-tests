FROM node:20-slim

ENV NODE_TLS_REJECT_UNAUTHORIZED=0
ENV OPENCODE_CONFIG_DIR=/opt/opencode-config

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g opencode-ai@1.14.19

COPY docker/opencode-config /opt/opencode-config

WORKDIR /runner
ENTRYPOINT ["opencode"]