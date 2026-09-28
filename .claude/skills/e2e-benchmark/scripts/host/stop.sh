# Runs on the Mac or a VM. Usage: host/stop.sh <out dir> <machine> <variant> [client]
set -euo pipefail
. "$(dirname "$0")/../lib.sh"
out=$1 m=$2 v=$3 client=${4-}
mkdir -p "$out" && out=$(cd "$out" && pwd)
[ -f "$out/server.pid" ] || die "no server.pid in $out"
pid=$(cat "$out/server.pid")
if [ -n "$client" ]; then
  echo "rss_kb_after_$client $(ps -o rss= -p "$pid" | tr -d ' ')" >>"$out/e2e/$m/$v/server.txt"
fi
kill "$pid" 2>/dev/null || true
while kill -0 "$pid" 2>/dev/null; do sleep 1; done
rm -f "$out/server.pid"
