# syntax=docker/dockerfile:1.18

FROM docker.io/library/golang:1.26.5-bookworm AS vend-builder
WORKDIR /src
COPY vend/go.mod vend/go.sum ./
RUN go mod download
COPY vend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vend .

# Build the appliance from the official Ubuntu base instead of inheriting the
# general-purpose exeuntu workstation. Forge needs systemd, Docker, its five
# login CLIs, and a small set of operational tools; browsers, Go, Pi, desktop
# libraries, and unrelated development runtimes made the previous image more
# than 1.5 GiB compressed. Pin the multi-platform manifest for reproducibility.
FROM docker.io/library/ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90

SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]
ENV DEBIAN_FRONTEND=noninteractive

# Minimal bootable exe.dev host plus the tools used by exe-image-forge itself.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg \
      git jq python3 \
      sudo openssh-server openssh-client \
      systemd systemd-sysv dbus dbus-user-session \
      docker.io docker-buildx containerd \
      locales tzdata \
      iproute2 procps psmisc lsof \
      util-linux xz-utils zstd && \
    sed -i 's/^# *\(en_US.UTF-8\)/\1/' /etc/locale.gen && locale-gen && \
    rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub && \
    rm -f /usr/sbin/policy-rc.d && \
    rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# GitHub CLI repository. The shared updater can then keep gh current using apt.
RUN install -d -m0755 /etc/apt/keyrings && \
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      -o /etc/apt/keyrings/githubcli.gpg && \
    chmod 0644 /etc/apt/keyrings/githubcli.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends gh && \
    rm -rf /var/lib/apt/lists/*

# Node supports Gemini and Wrangler. The global Wrangler is the bootstrap and
# login CLI; Cloudflare projects can still pin their own local version.
ARG NODE_MAJOR=24
RUN curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
      | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" \
      > /etc/apt/sources.list.d/nodesource.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Match the documented exe.dev custom-image conventions used by exeuntu:
# uid 1000, a systemd PID 1 wrapper, first-boot setup support, and Shelley
# socket activation. Shelley itself is injected by exe.dev because of the OCI
# label below; it is intentionally not baked into this image.
RUN if id ubuntu >/dev/null 2>&1; then \
      usermod -l exedev -c "exe.dev user" ubuntu && \
      groupmod -n exedev ubuntu && \
      mv /home/ubuntu /home/exedev && \
      usermod -d /home/exedev exedev && \
      sed -i 's/^ubuntu:/exedev:/' /etc/subuid /etc/subgid 2>/dev/null || true; \
    else \
      useradd -m -u 1000 -s /bin/bash -c "exe.dev user" exedev; \
    fi && \
    usermod -aG sudo,docker exedev && \
    echo 'exedev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/exedev && \
    chmod 0440 /etc/sudoers.d/exedev && \
    install -d -o exedev -g exedev -m0700 \
      /home/exedev/.config/gh \
      /home/exedev/.config/.wrangler/config \
      /home/exedev/.codex \
      /home/exedev/.claude \
      /home/exedev/.gemini \
      /home/exedev/.local/bin \
      /home/exedev/.config/shelley && \
    install -d /var/lib/systemd/linger && \
    touch /var/lib/systemd/linger/exedev /home/exedev/.hushlogin && \
    echo '/dev/vda / ext4 defaults,x-systemd.growfs 0 1' > /etc/fstab && \
    chown -R exedev:exedev /home/exedev

COPY image/files/tmpfiles-tmp.conf /etc/tmpfiles.d/tmp.conf
COPY image/files/shelley.socket /etc/systemd/system/shelley.socket
COPY image/files/shelley.service /etc/systemd/system/shelley.service
COPY image/files/exe-setup.service /etc/systemd/system/exe-setup.service
COPY image/files/init-wrapper.sh /usr/local/bin/init

# The same updater defines installation and the daily update behavior for every
# CLI. All five are needed on the appliance host because authentication must
# work before the generated development images have finished building.
COPY image/files/update-ai-clis /usr/local/bin/update-ai-clis
COPY image/files/write-versions /usr/local/bin/write-versions
RUN chmod 0755 /usr/local/bin/init /usr/local/bin/update-ai-clis \
      /usr/local/bin/write-versions && \
    INSTALL_MISSING=1 TOOLS="codex claude gemini gh wrangler" update-ai-clis && \
    codex --version && claude --version && gemini --version && \
    gh --version && wrangler --version

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
    systemctl mask -- \
      getty.target console-getty.service keyboard-setup.service \
      systemd-random-seed.service apt-daily.timer apt-daily-upgrade.timer \
      etc-resolv.conf.mount etc-hosts.mount etc-hostname.mount -.mount \
      systemd-resolved.service systemd-remount-fs.service \
      systemd-update-utmp.service systemd-udevd.service \
      systemd-udevd-control.service systemd-udevd-kernel.service \
      systemd-udev-trigger.service ssh.socket ssh.service && \
    install -d /etc/systemd/system.conf.d /etc/systemd/journald.conf.d && \
    printf '[Manager]\nLogLevel=info\nLogTarget=console\nSystemCallArchitectures=native\nDefaultOOMPolicy=continue\n' \
      > /etc/systemd/system.conf.d/container-overrides.conf && \
    printf '[Journal]\nStorage=persistent\n' \
      > /etc/systemd/journald.conf.d/persistent.conf && \
    systemctl set-default multi-user.target && \
    systemctl enable \
      shelley.socket exe-setup.service \
      docker.service containerd.service \
      exe-image-forge-initialize.service \
      exe-image-forge-registry.service \
      exe-image-forge-vend.service \
      exe-image-forge-bootstrap.service \
      exe-image-forge-gc.timer \
      exe-image-forge-refresh.timer && \
    rm -rf /usr/share/doc/* /usr/share/doc-base /usr/share/info \
      /usr/share/lintian /usr/share/man/?? /usr/share/man/??_* \
      /var/cache/debconf/*-old /var/lib/apt/lists/* /var/log/* /tmp/* && \
    find /usr/share/locale -maxdepth 1 -mindepth 1 -type d \
      ! -name 'en*' ! -name 'C*' -exec rm -rf {} + 2>/dev/null || true

ENV EXE_IMAGE_FORGE_APPLIANCE=1 EXEUNTU=1

EXPOSE 8000

LABEL org.opencontainers.image.title="Exe Image Forge Appliance" \
      org.opencontainers.image.description="Bootable exe.dev appliance for building and vending authenticated AI development images" \
      org.opencontainers.image.source="https://github.com/hunydev/exe-image-forge" \
      org.opencontainers.image.licenses="Apache-2.0" \
      dev.exe.image-forge.image="appliance" \
      exe.dev/login-user="exedev" \
      exe.dev/install-shelley="true"

CMD ["/usr/local/bin/init"]
