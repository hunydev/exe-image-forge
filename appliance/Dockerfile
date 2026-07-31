# syntax=docker/dockerfile:1.18

FROM docker.io/library/golang:1.26.5-bookworm AS vend-builder
WORKDIR /src
COPY vend/go.mod vend/go.sum ./
RUN go mod download
COPY vend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vend .

# exeuntu is a bootable exe.dev systemd image with Docker, Buildx, Go, GitHub
# CLI, Codex, and Claude already installed. Pin the multi-platform manifest so
# a release rebuild cannot silently change its operating-system base.
FROM ghcr.io/boldsoftware/exeuntu@sha256:699fba205efe2376f93a2db30187f454553b5c6bead7074b7dbf5eec7031fa01

SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]

# Gemini needs a current Node.js runtime. The appliance carries every login
# CLI on the host so authentication works while its generated images build.
ARG NODE_MAJOR=24
RUN install -d -m0755 /etc/apt/keyrings && \
    curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
      | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" \
      > /etc/apt/sources.list.d/nodesource.list && \
    apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

COPY image/files/update-ai-clis /usr/local/bin/update-ai-clis
COPY image/files/write-versions /usr/local/bin/write-versions
RUN chmod 0755 /usr/local/bin/update-ai-clis /usr/local/bin/write-versions && \
    INSTALL_MISSING=1 TOOLS=gemini update-ai-clis && \
    gemini --version

COPY --from=vend-builder /out/vend /opt/exe-image-forge/vend
COPY exe-image-forge /usr/local/bin/exe-image-forge
COPY image/ /opt/exe-image-forge/source/image/
COPY vend/ /opt/exe-image-forge/source/vend/
COPY deploy/ /opt/exe-image-forge/source/deploy/
COPY appliance/initialize.sh /usr/local/libexec/exe-image-forge-initialize
COPY appliance/start-registry.sh /usr/local/libexec/exe-image-forge-start-registry
COPY appliance/bootstrap.sh /usr/local/libexec/exe-image-forge-bootstrap
COPY appliance/first-login.sh /usr/local/bin/exe-image-forge-first-login
COPY appliance/exe-image-forge-initialize.service /etc/systemd/system/
COPY appliance/exe-image-forge-registry.service /etc/systemd/system/
COPY appliance/exe-image-forge-bootstrap.service /etc/systemd/system/
COPY deploy/exe-image-forge-vend.service /etc/systemd/system/
COPY deploy/exe-image-forge-gc.service /etc/systemd/system/
COPY deploy/exe-image-forge-gc.timer /etc/systemd/system/
COPY deploy/exe-image-forge-refresh.service /etc/systemd/system/
COPY deploy/exe-image-forge-refresh.timer /etc/systemd/system/
COPY appliance/motd.sh /etc/profile.d/exe-image-forge.sh

RUN chmod 0755 \
      /opt/exe-image-forge/vend \
      /usr/local/bin/exe-image-forge \
      /usr/local/bin/exe-image-forge-first-login \
      /usr/local/libexec/exe-image-forge-initialize \
      /usr/local/libexec/exe-image-forge-start-registry \
      /usr/local/libexec/exe-image-forge-bootstrap \
      /etc/profile.d/exe-image-forge.sh && \
    chmod 0644 /etc/systemd/system/exe-image-forge-* && \
    install -d -m0755 /opt/exe-image-forge/source && \
    systemctl enable \
      docker.service containerd.service \
      exe-image-forge-initialize.service \
      exe-image-forge-registry.service \
      exe-image-forge-vend.service \
      exe-image-forge-bootstrap.service \
      exe-image-forge-gc.timer \
      exe-image-forge-refresh.timer

ENV EXE_IMAGE_FORGE_APPLIANCE=1

EXPOSE 8000

LABEL org.opencontainers.image.title="Exe Image Forge Appliance" \
      org.opencontainers.image.description="Bootable exe.dev appliance for building and vending authenticated AI development images" \
      org.opencontainers.image.source="https://github.com/hunydev/exe-image-forge" \
      org.opencontainers.image.licenses="Apache-2.0" \
      dev.exe.image-forge.image="appliance" \
      exe.dev/login-user="exedev" \
      exe.dev/install-shelley="true"

CMD ["/usr/local/bin/init"]
