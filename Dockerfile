FROM php:8.3-cli-alpine

# ssh-клиент - единственное, что нужно кроме PHP
RUN apk add --no-cache openssh-client

COPY docker/askpass.sh /usr/local/bin/askpass
COPY docker/entrypoint.sh /entrypoint.sh
# sed на случай, если файлы сохранили с Windows-переводами строк (CRLF), иначе sh не запустит скрипт
RUN sed -i 's/\r$//' /usr/local/bin/askpass /entrypoint.sh \
    && chmod +x /usr/local/bin/askpass /entrypoint.sh

# askpass отдаёт ssh пароль из переменной SSH_PW (ссылка на неё в Tunnels::launch)
ENV SSH_ASKPASS=/usr/local/bin/askpass \
    SSH_ASKPASS_REQUIRE=force \
    DISPLAY=:0 \
    DATA_DIR=/data \
    PHP_CLI_SERVER_WORKERS=8

COPY app /app
WORKDIR /app
EXPOSE 8099
ENTRYPOINT ["/entrypoint.sh"]
