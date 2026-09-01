#!/usr/bin/env bash
# Copy this plugin into ~/.config/omarchy/plugins/<id>.
# PhoneCam's git root has no manifest.json, so `omarchy plugin add` of the
# product repo cannot work. This copies the plugin folder only.

set -euo pipefail

src=$(cd "$(dirname "$0")" && pwd)
id=io.github.kvm404.phonecam
dest="${XDG_CONFIG_HOME:-$HOME/.config}/omarchy/plugins/$id"

if ! command -v omarchy >/dev/null 2>&1; then
  echo "install.sh: omarchy CLI not found on PATH" >&2
  exit 1
fi

omarchy plugin validate "$src"

if [[ $src == "$dest" ]]; then
  echo "Already installed at $dest"
else
  mkdir -p "$(dirname "$dest")"
  rm -rf "$dest"
  mkdir -p "$dest"
  # Copy files. Do not symlink the plugin directory (validate forbids
  # symlinks inside an installed plugin).
  for entry in "$src"/*; do
    [[ -e $entry ]] || continue
    cp -a "$entry" "$dest/"
  done
fi

chmod +x "$dest/install.sh" "$dest/bin/phonecam-qr" "$dest/bin/phonecam-preview" 2>/dev/null || true

echo "Installed $dest"
if command -v omarchy-shell >/dev/null 2>&1; then
  omarchy-shell shell rescanPlugins >/dev/null 2>&1 || true
fi
echo "Enable with: omarchy plugin enable $id"
