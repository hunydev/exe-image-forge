# exe-image-forge base image: slim, exeuntu-compatible dev image.
# Derived in part from https://github.com/boldsoftware/exeuntu (Apache-2.0).
FROM ubuntu:24.04

SHELL ["/bin/bash", "-euxo", "pipefail", "-c"]

ENV DEBIAN_FRONTEND=noninteractive

# --- base packages -------------------------------------------------------
RUN apt-get update && \
    apt-get -y -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confdef dist-upgrade && \
    apt-get install -y --no-install-recommends \
        ca-certificates curl wget gnupg \
        git jq sqlite3 ripgrep tree file less vim \
        make build-essential pkg-config \
        python3 python3-venv python3-pip python-is-python3 pipx \
        sudo openssh-server openssh-client \
        systemd systemd-sysv dbus dbus-user-session \
        unzip zip xz-utils rsync tar \
        iproute2 iputils-ping netcat-openbsd socat lsof psmisc procps \
        locales tzdata libcap2-bin bash-completion man-db \
        && \
    sed -i 's/^# *\(en_US.UTF-8\)/\1/;s/^# *\(ko_KR.UTF-8\)/\1/' /etc/locale.gen && locale-gen && \
    setcap cap_net_raw=+ep /usr/bin/ping && \
    rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub && \
    rm -f /usr/sbin/policy-rc.d && \
    rm -rf /var/lib/apt/lists/*

ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# --- gh cli --------------------------------------------------------------
RUN mkdir -p /etc/apt/keyrings && \
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /etc/apt/keyrings/githubcli.gpg && \
    chmod 0644 /etc/apt/keyrings/githubcli.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y --no-install-recommends gh && \
    rm -rf /var/lib/apt/lists/*

# --- node (NodeSource LTS) ----------------------------------------------
ARG NODE_MAJOR=22
RUN curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_${NODE_MAJOR}.x nodistro main" > /etc/apt/sources.list.d/nodesource.list && \
    apt-get update && apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/* && \
    npm install -g npm@latest && npm cache clean --force && \
    node --version && npm --version && npx --version

# --- uv / uvx ------------------------------------------------------------
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh && \
    uv --version && uvx --version

# --- systemd trimming (from exeuntu) ------------------------------------
RUN rm -f /etc/systemd/system/multi-user.target.wants/console-setup.service \
          /etc/systemd/system/multi-user.target.wants/ModemManager.service \
          /etc/systemd/system/multi-user.target.wants/unattended-upgrades.* && \
    systemctl mask -- getty.target console-getty.service keyboard-setup.service \
        systemd-random-seed.service man-db.timer dpkg-db-backup.timer e2scrub_all.timer \
        etc-resolv.conf.mount etc-hosts.mount etc-hostname.mount -.mount \
        systemd-resolved.service systemd-remount-fs.service systemd-sysusers.service \
        systemd-update-done.service systemd-update-utmp.service \
        systemd-journal-catalog-update.service modprobe@.service systemd-modules-load.service \
        systemd-udevd.service systemd-udevd-control.service systemd-udevd-kernel.service \
        systemd-udev-trigger.service systemd-udev-settle.service systemd-hwdb-update.service \
        ldconfig.service systemd-ask-password-console.path systemd-ask-password-wall.path \
        ssh.socket ssh.service apt-daily.timer apt-daily-upgrade.timer && \
    systemctl disable getty@.service systemd-logind.service || true && \
    mkdir -p /etc/systemd/system.conf.d && \
    printf '[Manager]\nLogLevel=info\nLogTarget=console\nSystemCallArchitectures=native\nDefaultOOMPolicy=continue\n' > /etc/systemd/system.conf.d/container-overrides.conf && \
    mkdir -p /etc/systemd/journald.conf.d && \
    printf '[Journal]\nStorage=persistent\n' > /etc/systemd/journald.conf.d/persistent.conf && \
    systemctl set-default multi-user.target

# --- exedev user ---------------------------------------------------------
RUN if id ubuntu >/dev/null 2>&1; then \
        usermod -l exedev -c "exe.dev user" ubuntu && groupmod -n exedev ubuntu && \
        mv /home/ubuntu /home/exedev && usermod -d /home/exedev exedev && \
        sed -i 's/^ubuntu:/exedev:/' /etc/subuid /etc/subgid ; \
    else \
        useradd -m -u 1000 -s /bin/bash -c "exe.dev user" exedev ; \
    fi && \
    usermod -aG sudo exedev && \
    echo 'exedev ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers && \
    echo 'Defaults:exedev verifypw=any' >> /etc/sudoers && \
    mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/exedev && \
    echo '/dev/vda / ext4 defaults,x-systemd.growfs 0 1' > /etc/fstab && \
    rm -rf /etc/update-motd.d/* /etc/motd && \
    mkdir -p /home/exedev/.config/shelley /home/exedev/.local/bin && \
    touch /home/exedev/.hushlogin && \
    chown -R exedev:exedev /home/exedev

COPY files/tmpfiles-tmp.conf /etc/tmpfiles.d/tmp.conf
COPY files/shelley.socket /etc/systemd/system/shelley.socket
COPY files/shelley.service /etc/systemd/system/shelley.service
COPY files/exe-setup.service /etc/systemd/system/exe-setup.service
COPY files/init-wrapper.sh /usr/local/bin/init
RUN chmod 644 /etc/systemd/system/shelley.socket /etc/systemd/system/shelley.service /etc/systemd/system/exe-setup.service && \
    chmod 755 /usr/local/bin/init && \
    systemctl enable shelley.socket exe-setup.service

# --- ai agent clis -------------------------------------------------------
# The CLIs are installed by the same script that later updates them in place,
# so there is exactly one definition of "how do I get the current version" and
# the update path is exercised on every build.
COPY files/update-ai-clis /usr/local/bin/update-ai-clis
COPY files/write-versions /usr/local/bin/write-versions
COPY files/write-agent-context /usr/local/bin/write-agent-context
COPY files/agent-context.service /etc/systemd/system/agent-context.service
COPY files/ai-cli-update.service /etc/systemd/system/ai-cli-update.service
COPY files/ai-cli-update.timer /etc/systemd/system/ai-cli-update.timer
# CLI_REFRESH busts the layer cache when we want fresh CLI versions without
# rebuilding the (slow, stable) layers underneath.
ARG CLI_REFRESH=0
RUN chmod 755 /usr/local/bin/update-ai-clis /usr/local/bin/write-versions \
        /usr/local/bin/write-agent-context && \
    chmod 644 /etc/systemd/system/agent-context.service && \
    systemctl enable agent-context.service && \
    chmod 644 /etc/systemd/system/ai-cli-update.service /etc/systemd/system/ai-cli-update.timer && \
    systemctl enable ai-cli-update.timer && \
    echo "refresh=${CLI_REFRESH}" && \
    TOOLS="codex claude gh" /usr/local/bin/update-ai-clis && \
    codex --version && claude --version

# --- optional components -------------------------------------------------
# These are deliberately the last layers in the image. Everything above is
# shared by every variant, so a registry stores the expensive codex/claude
# blobs once no matter how many combinations are published.

# Gemini CLI (~100MB of node_modules).
ARG WITH_GEMINI=0
RUN if [ "$WITH_GEMINI" = "1" ]; then \
      INSTALL_MISSING=1 TOOLS=gemini /usr/local/bin/update-ai-clis && gemini --version ; \
    else echo "skipping gemini cli" ; fi

# Go toolchain (~240MB after dropping its test corpus and API history).
ARG WITH_GO=0
RUN if [ "$WITH_GO" = "1" ]; then \
      ARCH=$(dpkg --print-architecture) && \
      GO_VERSION=$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '.[0].version') && \
      curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -xzC /usr/local && \
      ln -s /usr/local/go/bin/go /usr/local/bin/go && \
      ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt && \
      rm -rf /usr/local/go/test /usr/local/go/api /usr/local/go/doc && \
      go version ; \
    else echo "skipping go toolchain" ; fi


# Record the final tool set, now that the optional pieces are decided.
ARG VARIANT=min
RUN echo "$VARIANT" > /etc/exe-image-forge-variant && \
    /usr/local/bin/write-versions && cat /etc/ai-cli-versions.json && \
    /usr/local/bin/write-agent-context && \
    chown -R 1000:1000 /home/exedev/.codex /home/exedev/.claude /home/exedev/.gemini

# --- slimming ------------------------------------------------------------
# Documentation, changelogs and vim's runtime are dead weight in an image whose
# whole purpose is to be pulled repeatedly over the network. Removed last so it
# also catches anything the CLI installs dropped.
RUN rm -rf /usr/share/doc/* /usr/share/doc-base /usr/share/info \
           /usr/share/lintian /usr/share/linda /usr/share/man/?? /usr/share/man/??_* \
           /var/cache/debconf/*-old /var/lib/apt/lists/* /var/log/* /tmp/* && \
    find /usr/share/locale -maxdepth 1 -mindepth 1 -type d \
         ! -name 'en*' ! -name 'ko*' ! -name 'C*' -exec rm -rf {} + 2>/dev/null || true

# --- user config ---------------------------------------------------------
USER exedev
WORKDIR /home/exedev
RUN mkdir -p /home/exedev/.claude /home/exedev/.codex /home/exedev/.gemini /home/exedev/.config/gh && \
    echo 'export PATH="$HOME/.local/bin:$PATH"' >> /home/exedev/.bashrc && \
    echo 'export XDG_RUNTIME_DIR="/run/user/$(id -u)"' >> /home/exedev/.bashrc && \
    echo 'export XDG_RUNTIME_DIR="/run/user/$(id -u)"' >> /home/exedev/.profile && \
    git config --global init.defaultBranch main

USER root
ENV EXEUNTU=1
EXPOSE 8000 9999
LABEL "exe.dev/login-user"="exedev"
LABEL "exe.dev/install-shelley"="true"
LABEL "dev.exe.image-forge.image"="base"
CMD ["/usr/local/bin/init"]
