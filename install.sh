#!/bin/sh
set -eu
# Linux base nodes only. Full OpenClaw assistants are validated on Windows first.
version=0.1.0-alpha.1
prefix=${ASTERHAND_INSTALL_DIR:-"$HOME/.local/opt/asterhand"}
package=${1:-}
manifest=${2:-}
if [ "$(uname -s)" != Linux ] || [ "$(uname -m)" != x86_64 ]; then
  echo 'This artifact is a Linux x86_64 base node; other platforms are not validated.' >&2; exit 1
fi
for tool in curl sha256sum tar python3; do command -v "$tool" >/dev/null || { echo "Required installer tool missing: $tool" >&2; exit 1; }; done
prefix=$(python3 - "$prefix" <<'PY'
import os,sys
p=os.path.realpath(sys.argv[1])
if p in ('/',os.path.expanduser('~')): raise SystemExit('Use a dedicated application directory.')
if os.path.isdir(p) and os.listdir(p) and not os.path.isfile(os.path.join(p,'.asterhand-install')):
    raise SystemExit('Install directory is nonempty and not owned by Asterhand.')
print(p)
PY
)
mkdir -p "$prefix/downloads"
printf 'Asterhand installer schema 1\n' > "$prefix/.asterhand-install"
base="https://github.com/jackgan90/Asterhand/releases/download/v$version"
if [ -z "$manifest" ]; then
  manifest="$prefix/downloads/release-manifest.json"
  curl --fail --location --proto '=https' --tlsv1.2 "$base/release-manifest.json" -o "$manifest"
fi
name="asterhand-$version-linux-amd64-base.tar.gz"
expected=$(python3 - "$manifest" "$version" "$name" <<'PY'
import json, re, sys
data = json.load(open(sys.argv[1], encoding='utf-8-sig'))
assert data['schema'] == 1 and data['version'] == sys.argv[2]
entry = next(x for x in data['packages'] if x['os'] == 'linux' and x['arch'] == 'amd64')
assert entry['file'] == sys.argv[3] and re.fullmatch('[a-fA-F0-9]{64}', entry['sha256'])
print(entry['sha256'])
PY
)
if [ -z "$package" ]; then
  package="$prefix/downloads/$name"
  curl --fail --location --proto '=https' --tlsv1.2 "$base/$name" -o "$package"
fi
printf '%s  %s\n' "$expected" "$package" | sha256sum --check -
destination="$prefix/$version-$(printf '%s' "$expected" | cut -c1-12)"
if [ ! -f "$destination/asterhand" ]; then
  stage=$(mktemp -d "$prefix/.staging-XXXXXXXX")
  python3 - "$package" "$stage" <<'PY'
import tarfile,sys,pathlib
with tarfile.open(sys.argv[1],'r:gz') as archive:
    for item in archive.getmembers():
        path=pathlib.PurePosixPath(item.name)
        if path.is_absolute() or '..' in path.parts or not (item.isfile() or item.isdir()):
            raise SystemExit('Unsafe release archive member.')
    archive.extractall(sys.argv[2],filter='data')
PY
  chmod 755 "$stage/asterhand"
  "$stage/asterhand" version | grep -Fx "$version" >/dev/null
  mv "$stage" "$destination"
fi
printf 'Installed base node. Run:\n  %s setup --basic\n' "$destination/asterhand"
printf 'This Linux artifact is cross-compiled. Real Linux runtime validation is recorded separately.\n'
