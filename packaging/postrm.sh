#!/bin/sh
# Package post-remove: reload systemd; keep the user, data, logs and config.
[ -d /run/systemd/system ] && systemctl daemon-reload || true
exit 0
