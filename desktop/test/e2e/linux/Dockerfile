# Native-Linux evidence runner for the packaged desktop app.
#
# Mirrors the ubuntu CI desktop leg: Node 22 + Go (go.mod toolchain) + xvfb +
# the Electron runtime libraries, so the repo can be built, packaged
# (AppImage + deb + linux-unpacked), and driven by the packaged Playwright
# journeys entirely on a real Linux kernel/userland. Used by
# run-linux-evidence.sh; not part of the shipped application.
# node:22-bookworm image index, pinned so the amd64 and arm64 builds use the
# corresponding immutable official image manifests.
FROM node:22-bookworm@sha256:5647be709086c696ff32edaaf1c70cd26d1da6ab2b39c32f3c7b4c4a31957e37

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl git xz-utils \
    xvfb xauth dbus dbus-x11 \
    libgtk-3-0 libnotify4 libnss3 libxss1 libxtst6 libatspi2.0-0 \
    libdrm2 libgbm1 libasound2 libsecret-1-0 libcups2 xdg-utils \
    fontconfig fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# Go toolchain matching go.mod (go 1.25.0). Check the official archive hash
# before unpacking so a corrupted or substituted download stops the build.
ARG GO_VERSION=1.25.0
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) GO_SHA256=2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613 ;; \
      arm64) GO_SHA256=05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae ;; \
      *) echo "unsupported Go target architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
    && GO_ARCHIVE="go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    && curl -fsSLO "https://go.dev/dl/${GO_ARCHIVE}" \
    && echo "${GO_SHA256}  ${GO_ARCHIVE}" | sha256sum -c - \
    && tar -C /usr/local -xzf "${GO_ARCHIVE}" \
    && rm "${GO_ARCHIVE}"
ENV PATH="/usr/local/go/bin:${PATH}"

# git identity for the journey worlds' repo fixtures.
RUN git config --system user.name "Agentico CI" \
    && git config --system user.email "ci@agentico.invalid" \
    && git config --system init.defaultBranch main

WORKDIR /work
