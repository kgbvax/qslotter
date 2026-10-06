#!/bin/sh
# Deploy the Windows build to the shack PC (bwpc) in one go:
#
#   scripts/deploy-bwpc.sh          build (release.sh), then deploy
#   scripts/deploy-bwpc.sh -n       deploy the exe already in dist/
#
# Steps: clean shutdown via /api/quit (closes the DB) and wait for the process
# to exit; back up qslotter.exe + qslotter.db* to qslotter\backup-<time>-<ver>;
# copy the new exe and compare its SHA-256; start the scheduled task; wait for
# the UI to answer 200. Stops at the first failing step (the old exe stays in
# place until the backup exists and the process is gone).
#
# Remote PowerShell is sent as -EncodedCommand (UTF-16LE base64): the
# German-locale cmd mangles quotes. HOST=user@host overrides the target.
# shellcheck disable=SC2016 # the PowerShell scripts expand on the target
set -eu
cd "$(dirname "$0")/.."

HOST="${HOST:-iotte@bwpc}"
EXE=dist/qslotter-windows-amd64.exe
SSH="ssh -o ConnectTimeout=30 -o LogLevel=ERROR"

build=1
case "${1:-}" in
-n) build=0 ;;
"") ;;
*) echo "usage: $0 [-n]" >&2; exit 2 ;;
esac

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
case "$VERSION" in *-dirty) echo "warning: uncommitted changes - deploying $VERSION" ;; esac
if [ "$build" = 1 ]; then
	scripts/release.sh
fi
[ -f "$EXE" ] || { echo "no $EXE - run without -n" >&2; exit 1; }

# remote runs a PowerShell script on the target; a failure there fails here.
remote() {
	enc=$(printf '%s' "\$ErrorActionPreference='Stop'; \$ProgressPreference='SilentlyContinue'; $1" | iconv -f UTF-8 -t UTF-16LE | base64 | tr -d '\n')
	$SSH "$HOST" "powershell -NoProfile -NonInteractive -EncodedCommand $enc"
}

step() { printf '\n== %s\n' "$*"; }

step "stop qslotter on $HOST"
remote '
try { Invoke-WebRequest -UseBasicParsing -Method Post http://127.0.0.1:8473/api/quit -TimeoutSec 10 | Out-Null; "quit sent" }
catch { "not answering (" + $_.Exception.Message + ") - maybe not running" }
for ($i = 0; $i -lt 30; $i++) {
  if (-not (Get-Process qslotter -ErrorAction SilentlyContinue)) { "stopped"; exit 0 }
  Start-Sleep 1
}
"qslotter.exe still running after 30 s - not touching the files"; exit 1'

BACKUP="backup-$(date +%Y%m%d-%H%M%S)-$VERSION"
step "back up to qslotter\\$BACKUP"
remote "
\$d = Join-Path \$HOME 'qslotter'
\$b = Join-Path \$d '$BACKUP'
New-Item -ItemType Directory -Force \$b | Out-Null
Copy-Item (Join-Path \$d 'qslotter.exe') \$b
Get-ChildItem \$d -Filter 'qslotter.db*' | Copy-Item -Destination \$b
if (-not (Test-Path (Join-Path \$b 'qslotter.db'))) { 'qslotter.db missing from the backup'; exit 1 }
Get-ChildItem \$b | Format-Table Name, Length, LastWriteTime -AutoSize | Out-String -Width 120"

step "copy $EXE ($VERSION)"
scp -q -o ConnectTimeout=30 -o LogLevel=ERROR "$EXE" "$HOST:qslotter/qslotter.exe"
want=$(shasum -a 256 "$EXE" | cut -d' ' -f1)
remote "
\$h = (Get-FileHash (Join-Path \$HOME 'qslotter\\qslotter.exe') -Algorithm SHA256).Hash.ToLower()
if (\$h -ne '$want') { 'SHA-256 mismatch: ' + \$h; exit 1 }
'SHA-256 ok'"

step "start the scheduled task"
remote '
schtasks /run /tn qslotter | Out-Null
for ($i = 0; $i -lt 30; $i++) {
  try { if ((Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8473/ -TimeoutSec 3).StatusCode -eq 200) { "up: 200"; exit 0 } } catch {}
  Start-Sleep 1
}
"not answering after 30 s - see %LOCALAPPDATA%\qslotter\qslotter.log"; exit 1'

printf '\ndeployed %s to %s (backup: qslotter\\%s)\n' "$VERSION" "$HOST" "$BACKUP"
