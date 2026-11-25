#!/usr/bin/env bash
set -euo pipefail

state="${RUNNER_TEMP:?RUNNER_TEMP is required}/ethos-opencode"
root=$(git rev-parse --show-toplevel)
workspace=$(dirname "$root")
export HOME="$state/home"
export XDG_CONFIG_HOME="$HOME/.config" XDG_CACHE_HOME="$HOME/.cache"
export XDG_DATA_HOME="$HOME/.local/share" XDG_STATE_HOME="$HOME/.local/state"
export XDG_RUNTIME_DIR="$state/runtime" TMPDIR="$state/tmp"
export PATH="$state/bin:$HOME/.local/bin:$PATH"
export UV_PYTHON=3.13.12

fetch_sibling() {
  local repo=$1 revision=$2 dir="$workspace/$1"
  git init --quiet "$dir"
  git -C "$dir" fetch --quiet --depth=1 "https://github.com/punt-labs/$repo.git" "$revision"
  git -C "$dir" checkout --quiet --detach FETCH_HEAD
}

install_nightly() {
  install -d -m 700 "$state/bin" "$HOME" "$XDG_RUNTIME_DIR" "$TMPDIR"
  sudo apt-get update -qq
  sudo apt-get install -y --no-install-recommends bison build-essential direnv espeak-ng ffmpeg flex libgl1 libgl1-mesa-dri mpv xauth xvfb

  local archive="$state/opencode-linux-x64.tar.gz"
  curl -fsSL --retry 3 -o "$archive" https://github.com/anomalyco/opencode/releases/download/v2.2.5/opencode-linux-x64.tar.gz
  printf '%s  %s\n' 09f44302c10fd036912f59c81fd1aef5df45dd06f649197f9dfc9bbe2a79614e "$archive" | sha256sum --check
  tar -xzf "$archive" -C "$state/bin" opencode
  opencode --version

  fetch_sibling prfaq 85fa8e3b009c2fecfb9859ace7d8bb0bca0ce708
  fetch_sibling z-spec dc3fec3d94ba7d6706b2626f38f447dcc6e7c5e7
  fetch_sibling punt-kit 8af110f87d61895beb0bc6a129da5cfe0c3ad613
  fetch_sibling beadle 02c64874215a1cbd9c091d38093e0478de237f6b

  uv python install "$UV_PYTHON"
  uv tool install 'punt-kit==0.19.0'
  uv tool install 'punt-vox==5.1.0'
  uv tool install 'punt-lux[display]==0.33.1'
  uv tool install 'punt-quarry==3.9.1'
  sh "$workspace/z-spec/install.sh" --no-plugin

  GOBIN="$state/bin" go install golang.org/x/tools/gopls@v0.21.1
  (
    cd "$workspace/beadle"
    go build -o "$state/bin/beadle-email" ./cmd/beadle-email
    install -D "$root/scripts/testdata/opencode-nightly-imap/imap_test.go" internal/nightlyimap/imap_test.go
    go test -c -o "$state/bin/nightly-imap" ./internal/nightlyimap
  )

  direnv allow "$root"
}

await_address() {
  local address_file=$1 pid=$2
  local attempt
  for ((attempt = 0; attempt < 100; attempt++)); do
    [[ ! -s "$address_file" ]] || return 0
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.1
  done
  echo "::error::Could not start the local Nightly IMAP fixture" >&2
  return 1
}

run_smoke() {
  : "${BIFROST_VK:?Set the BIFROST_VK Actions secret}"
  : "${BIFROST_BASE_URL:?Set the BIFROST_BASE_URL Actions variable}"
  : "${CONTEXT7_API_KEY:?Set the CONTEXT7_API_KEY Actions secret}"
  if [[ "$BIFROST_BASE_URL" != https://*/openai/v1 ]]; then
    echo "::error::BIFROST_BASE_URL must be an HTTPS base URL ending in /openai/v1" >&2
    return 1
  fi
  umask 077
  install -D "$root/scripts/testdata/opencode-nightly.json" "$XDG_CONFIG_HOME/opencode/opencode.json"
  export NIGHTLY_IMAP_ADDR_FILE="$state/imap-address"
  export BEADLE_IMAP_PASSWORD=nightly-fixture

  export NIGHTLY_VOX_DIR="$state/vox"
  export NIGHTLY_VOX_BIN
  NIGHTLY_VOX_BIN=$(command -v vox)
  mkdir -p "$NIGHTLY_VOX_DIR/.punt-labs/vox" "$state/wrappers"
  printf '%s\n' '---' 'provider: espeak' 'notify: "n"' 'speak: "n"' '---' >"$NIGHTLY_VOX_DIR/.punt-labs/vox/vox.md"
  cat >"$state/wrappers/vox" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cd "$NIGHTLY_VOX_DIR"
exec "$NIGHTLY_VOX_BIN" "$@"
EOF
  chmod +x "$state/wrappers/vox"
  export PATH="$state/wrappers:$PATH"
  trap 'opencode service stop >/dev/null 2>&1 || true; jobs -pr | xargs -r kill' EXIT

  # Do not pass the live credentials to local daemons.
  local -a local_env=(env -u BIFROST_VK -u CONTEXT7_API_KEY)
  "${local_env[@]}" nightly-imap -test.run '^TestServeNightlyIMAP$' -test.timeout=0 >"$state/imap.log" 2>&1 &
  await_address "$NIGHTLY_IMAP_ADDR_FILE" "$!"
  local address
  address=$(<"$NIGHTLY_IMAP_ADDR_FILE")
  [[ "$address" =~ ^127\.0\.0\.1:([0-9]+)$ ]]
  mkdir -p "$HOME/.punt-labs/ethos/identities" "$HOME/.punt-labs/beadle"
  printf 'handle: claude\nname: Nightly Fixture\nemail: nightly@example.com\n' >"$HOME/.punt-labs/ethos/identities/claude.yaml"
  jq -n --argjson port "${BASH_REMATCH[1]}" '{
    imap_host: "127.0.0.1", imap_port: $port, imap_user: "nightly@example.com",
    from_address: "nightly@example.com", poll_interval: "1m", telemetry_disabled: true
  }' >"$HOME/.punt-labs/beadle/email.json"

  export VOXD_PORT=18421
  "${local_env[@]}" voxd --port "$VOXD_PORT" >"$state/vox.log" 2>&1 &
  "${local_env[@]}" quarryd --port 0 >"$state/quarry.log" 2>&1 &
  "${local_env[@]}" luxd-hub >"$state/lux.log" 2>&1 &
  "${local_env[@]}" env LIBGL_ALWAYS_SOFTWARE=1 xvfb-run -a luxd-display >"$state/lux-display.log" 2>&1 &

  opencode service start
  make smoke-opencode
}

case "${1:-}" in
  install) install_nightly ;;
  smoke) run_smoke ;;
  *) echo "usage: $0 {install|smoke}" >&2; exit 2 ;;
esac
