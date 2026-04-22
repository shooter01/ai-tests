FROM node:20-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g opencode-ai

# entrypoint-скрипт — клонирует репо и зовёт opencode
COPY review-entrypoint.sh /usr/local/bin/review-entrypoint.sh
RUN chmod +x /usr/local/bin/review-entrypoint.sh

# чтобы git не спрашивал логин/пароль в TTY
ENV GIT_TERMINAL_PROMPT=0

ENTRYPOINT ["/usr/local/bin/review-entrypoint.sh"]