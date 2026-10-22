#!/bin/sh
# Install voidrift from an unpacked release tarball: binaries, man pages,
# service user, data/log/config directories and the init integration for this
# system (systemd or OpenRC). Does not enable or start the service — configure
# /etc/voidrift/voidrift.env first.
# Usage: sudo ./install.sh [--uninstall]      (PREFIX defaults to /usr)
set -e

PREFIX=${PREFIX:-/usr}
BINDIR=${BINDIR:-$PREFIX/bin}
MANDIR=${MANDIR:-$PREFIX/share/man}
here=$(cd "$(dirname "$0")" && pwd)

[ "$(id -u)" = 0 ] || { echo "install.sh: run as root" >&2; exit 1; }

if [ -f /etc/alpine-release ] && command -v rc-service >/dev/null 2>&1; then
	init=openrc
elif [ -d /run/systemd/system ]; then
	init=systemd
else
	init=none
fi

if [ "$1" = "--uninstall" ]; then
	case $init in
	systemd)
		systemctl disable --now voidrift 2>/dev/null || true
		rm -f /etc/systemd/system/voidrift.service
		systemctl daemon-reload ;;
	openrc)
		rc-service voidrift stop 2>/dev/null || true
		rc-update del voidrift default 2>/dev/null || true
		rm -f /etc/init.d/voidrift ;;
	esac
	rm -f "$BINDIR/voidrift" "$BINDIR/drifter" /etc/logrotate.d/voidrift "$MANDIR/man1/voidrift.1" "$MANDIR/man1/drifter.1"
	echo "Removed. Data (/var/lib/voidrift), logs, /etc/voidrift and the voidrift user were left in place."
	exit 0
fi

# Service user. OpenRC runs as voidrift:nogroup, systemd as voidrift:voidrift.
if ! id voidrift >/dev/null 2>&1; then
	if [ "$init" = openrc ]; then
		adduser -S -D -h /var/lib/voidrift -s /sbin/nologin -G nogroup voidrift
	else
		useradd -r -U -d /var/lib/voidrift -s /usr/sbin/nologin voidrift
	fi
fi
grp=$(id -gn voidrift)

install -Dm755 "$here/voidrift" "$BINDIR/voidrift"
install -Dm755 "$here/drifter" "$BINDIR/drifter"
install -Dm644 "$here/voidrift.1" "$MANDIR/man1/voidrift.1"
install -Dm644 "$here/drifter.1" "$MANDIR/man1/drifter.1"
install -d -m750 -o voidrift -g "$grp" /var/lib/voidrift /var/log/voidrift
install -d -m700 /etc/voidrift
install -m600 "$here/voidrift.env.example" /etc/voidrift/voidrift.env.example
[ -d /etc/logrotate.d ] && install -m644 "$here/voidrift.logrotate" /etc/logrotate.d/voidrift

case $init in
systemd)
	sed "s|/usr/bin/voidrift|$BINDIR/voidrift|" "$here/voidrift.service" > /etc/systemd/system/voidrift.service
	chmod 644 /etc/systemd/system/voidrift.service
	systemctl daemon-reload
	start="systemctl enable --now voidrift" ;;
openrc)
	sed "s|/usr/bin/voidrift|$BINDIR/voidrift|" "$here/voidrift.openrc" > /etc/init.d/voidrift
	chmod 755 /etc/init.d/voidrift
	start="rc-update add voidrift default && rc-service voidrift start" ;;
*)
	start="" ;;
esac

echo "Installed to $BINDIR. Next:"
echo "  cp /etc/voidrift/voidrift.env.example /etc/voidrift/voidrift.env && chmod 600 /etc/voidrift/voidrift.env"
echo "  \$EDITOR /etc/voidrift/voidrift.env     # server, nick, channel, passwords"
[ -n "$start" ] && echo "  $start" || echo "  (no systemd/OpenRC found: run $BINDIR/voidrift directly)"
