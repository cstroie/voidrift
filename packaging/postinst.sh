#!/bin/sh
# Package post-install: create the service user and the data, log and config
# directories. Never starts or enables the service — the defaults would put an
# unconfigured bot on a public network.
set -e
if ! id voidrift >/dev/null 2>&1; then
	if [ -f /etc/alpine-release ]; then
		adduser -S -D -h /var/lib/voidrift -s /sbin/nologin -G nogroup voidrift
	else
		useradd -r -U -d /var/lib/voidrift -s /usr/sbin/nologin voidrift
	fi
fi
grp=$(id -gn voidrift)
install -d -m750 -o voidrift -g "$grp" /var/lib/voidrift /var/log/voidrift
install -d -m700 /etc/voidrift
chmod 600 /etc/voidrift/voidrift.env.example 2>/dev/null || true
[ -d /run/systemd/system ] && systemctl daemon-reload || true
echo "voidrift installed. Configure /etc/voidrift/voidrift.env (copy voidrift.env.example, chmod 600), then:"
if [ -f /etc/alpine-release ]; then
	echo "  rc-update add voidrift default && rc-service voidrift start"
else
	echo "  systemctl enable --now voidrift"
fi
