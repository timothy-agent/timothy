# syntax=docker/dockerfile:1

# PHP environment variant (tag timothy-sandbox-php): adds PHP 8.4 and
# Composer 2 to the base mission sandbox, with the extensions Laravel
# and PHPUnit need (sqlite for tests).
ARG SANDBOX_BASE=timothy-sandbox-base:latest
FROM composer:2.8.12 AS composer-dist

FROM ${SANDBOX_BASE}

USER root

# PHP 8.4 from the Sury apt repo, which tracks PHP independently of the
# Debian release. Key is downloaded to a file, then installed as a
# keyring. Dev headers and build tools are never installed.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && . /etc/os-release \
    && curl -fsSL -o /tmp/sury.gpg https://packages.sury.org/php/apt.gpg \
    && install -m 0644 /tmp/sury.gpg /usr/share/keyrings/sury-php.gpg \
    && rm /tmp/sury.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${VERSION_CODENAME} main" \
        > /etc/apt/sources.list.d/sury-php.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        php8.4-cli php8.4-mbstring php8.4-xml php8.4-sqlite3 php8.4-curl \
        php8.4-zip php8.4-intl php8.4-bcmath \
    && rm -rf /var/lib/apt/lists/*

# Composer phar from the official image. COMPOSER_HOME is under the
# writable sandbox home so uid 65534 can cache and write config.
COPY --from=composer-dist /usr/bin/composer /usr/local/bin/composer
ENV COMPOSER_HOME=/home/sandbox/.composer

USER 65534:65534
