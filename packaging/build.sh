#!/bin/sh
# Build the release archive and linux packages for one architecture.
# Usage: packaging/build.sh <goarch> <version>   e.g. amd64 v261022
# Expects ./voidrift-linux-<goarch> and ./drifter-linux-<goarch> (fresh builds:
# `make dist` leaves stale ones around); writes into ./dist.
# Packages need nfpm (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest).
set -e
arch=$1; tag=$2
[ -n "$arch" ] && [ -n "$tag" ] || { echo "usage: $0 <goarch> <version>" >&2; exit 1; }
ver=${tag#v}
for b in voidrift drifter; do
	[ -f "$b-linux-$arch" ] || { echo "$b-linux-$arch not found" >&2; exit 1; }
done

name=voidrift-$tag-linux-$arch
rm -rf "dist/$name"; mkdir -p "dist/$name"
cp "voidrift-linux-$arch" "dist/$name/voidrift"
cp "drifter-linux-$arch" "dist/$name/drifter"
cp man/man1/voidrift.1 man/man1/drifter.1 README.md LICENSE "dist/$name/"
cp init/voidrift.service init/voidrift.openrc init/voidrift.env.example "dist/$name/"
cp packaging/install.sh "dist/$name/install.sh"
chmod 755 "dist/$name/install.sh" "dist/$name/voidrift" "dist/$name/drifter"
tar -C dist -czf "dist/$name.tar.gz" "$name"
rm -rf "dist/$name"

if command -v nfpm >/dev/null 2>&1; then
	# nfpm does not expand variables in file paths, so substitute up front.
	sed "s|\${GOARCH}|$arch|g; s|\${VERSION}|$ver|g" packaging/nfpm.yaml > dist/nfpm.yaml
	for p in deb rpm apk; do
		nfpm package -f dist/nfpm.yaml -p $p -t dist/
	done
	rm -f dist/nfpm.yaml
else
	echo "nfpm not found: skipped deb/rpm/apk" >&2
fi
