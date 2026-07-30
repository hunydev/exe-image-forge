# hunydev base image: slim, exeuntu-compatible dev image.
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

# --- go ------------------------------------------------------------------
RUN ARCH=$(dpkg --print-architecture) && \
    GO_VERSION=$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '.[0].version') && \
    curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -xzC /usr/local && \
    ln -s /usr/local/go/bin/go /usr/local/bin/go && \
    ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt && go version

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
# codex (native binary)
RUN case "$(uname -m)" in \
      x86_64) A=x86_64-unknown-linux-musl ;; \
      aarch64|arm64) A=aarch64-unknown-linux-musl ;; \
      *) echo unsupported; exit 1 ;; \
    esac && \
    V=$(curl -fsSL https://api.github.com/repos/openai/codex/releases/latest | jq -r .tag_name) && \
    curl -fsSL "https://github.com/openai/codex/releases/download/${V}/codex-${A}.tar.gz" | tar -xzC /usr/local/bin && \
    mv "/usr/local/bin/codex-${A}" /usr/local/bin/codex && chmod +x /usr/local/bin/codex && \
    codex --version

# gemini cli (npm, global)
RUN npm install -g @google/gemini-cli && npm cache clean --force && gemini --version

# claude code (official installer, into /usr/local)
RUN curl -fsSL https://claude.ai/install.sh -o /tmp/claude-install.sh && \
    bash /tmp/claude-install.sh latest && rm -f /tmp/claude-install.sh && \
    ( test -x /root/.local/bin/claude && install -m0755 /root/.local/bin/claude /usr/local/bin/claude || true ) && \
    rm -rf /root/.local/share/claude /root/.local/bin/claude && \
    claude --version

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
LABEL "dev.huny.image"="base"
CMD ["/usr/local/bin/init"]
