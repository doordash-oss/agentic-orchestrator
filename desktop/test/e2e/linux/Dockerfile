# Native-Linux evidence runner for the packaged desktop app.
#
# Mirrors the ubuntu CI desktop leg: Node 22 + Go (go.mod toolchain) + xvfb +
# the Electron runtime libraries, so the repo can be built, packaged
# (AppImage + deb + linux-unpacked), and driven by the packaged Playwright
# journeys entirely on a real Linux kernel/userland. Used by
# run-linux-evidence.sh; not part of the shipped application.
FROM node:22-bookworm

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git xz-utils \
    xvfb xauth dbus dbus-x11 \
    libgtk-3-0 libnotify4 libnss3 libxss1 libxtst6 libatspi2.0-0 \
    libdrm2 libgbm1 libasound2 libsecret-1-0 libcups2 xdg-utils \
    fontconfig fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain matching go.mod (go 1.25.0).
ARG GO_VERSION=1.25.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-$(dpkg --print-architecture | sed 's/amd64/amd64/;s/arm64/arm64/').tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# git identity for the journey worlds' repo fixtures.
RUN git config --system user.name "Agentico CI" \
    && git config --system user.email "ci@agentico.invalid" \
    && git config --system init.defaultBranch main

WORKDIR /work
