#!/bin/sh
set -eu

version=""
uninstall=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) version=${2:?--version requires a value}; shift 2 ;;
    --uninstall) uninstall=1; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

install_dir="$HOME/.local/bin"
binary="$install_dir/skillctl"
shell_name=$(basename "${SHELL:-sh}")
case "$shell_name" in
  bash) rc="$HOME/.bashrc"; path_line='export PATH="$HOME/.local/bin:$PATH"' ;;
  zsh) rc="$HOME/.zshrc"; path_line='export PATH="$HOME/.local/bin:$PATH"' ;;
  fish) rc="$HOME/.config/fish/config.fish"; path_line='set -gx PATH $HOME/.local/bin $PATH' ;;
  *) rc=""; path_line="" ;;
esac

remove_path() {
  [ -n "$rc" ] && [ -f "$rc" ] || return 0
  temp="$rc.skillctl.tmp"
  awk '/^# skillctl PATH begin$/{skip=1;next}/^# skillctl PATH end$/{skip=0;next}!skip{print}' "$rc" > "$temp"
  mv "$temp" "$rc"
}

if [ "$uninstall" -eq 1 ]; then
  rm -f "$binary"
  remove_path
  echo "skillctl was uninstalled. Configuration and skills were preserved."
  exit 0
fi

if [ -z "$version" ]; then
  latest=$(curl -fsSL -o /dev/null -w '%{url_effective}' https://github.com/lingengyuan/skillctl/releases/latest)
  version=${latest##*/}
fi
plain_version=${version#v}
os=$(uname -s | tr '[:upper:]' '[:lower:]')
[ "$os" = "darwin" ] || [ "$os" = "linux" ] || { echo "unsupported OS: $os" >&2; exit 1; }
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
archive="skillctl_${plain_version}_${os}_${arch}.tar.gz"
base="https://github.com/lingengyuan/skillctl/releases/download/$version"
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT INT TERM
curl -fsSL "$base/$archive" -o "$temp/$archive"
curl -fsSL "$base/SHA256SUMS" -o "$temp/SHA256SUMS"
expected=$(awk -v file="$archive" '$2 == file { print $1; exit }' "$temp/SHA256SUMS")
[ -n "$expected" ] || { echo "no checksum found for $archive" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$temp/$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$temp/$archive" | awk '{print $1}')
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi
[ "$expected" = "$actual" ] || { echo "checksum verification failed" >&2; exit 1; }
tar -xzf "$temp/$archive" -C "$temp"
mkdir -p "$install_dir"
install -m 0755 "$temp/skillctl" "$binary"
if [ -n "$rc" ]; then
  mkdir -p "$(dirname "$rc")"
  touch "$rc"
  if ! grep -Fq '# skillctl PATH begin' "$rc"; then
    printf '\n# skillctl PATH begin\n%s\n# skillctl PATH end\n' "$path_line" >> "$rc"
  fi
else
  echo "warning: unsupported shell; add $install_dir to PATH manually" >&2
fi
echo "Installed skillctl $version. Open a new terminal to use it."
