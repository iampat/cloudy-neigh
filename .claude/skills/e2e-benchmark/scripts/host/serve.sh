# Runs on the Mac or a VM. Usage: host/serve.sh <out dir> <machine> <variant> <store url> <client>
# Leaves the query server up for a remote client. host/stop.sh stops it.
set -euo pipefail
. "$(dirname "$0")/../lib.sh"
out=$1 m=$2 v=$3 store=$4 client=$5
mkdir -p "$out" && out=$(cd "$out" && pwd)
d=$out/e2e/$m/$v
mkdir -p "$d/$client"
read -r cpu ccpu <<<"$(pin_layout)"
bin=$(bin_dir "$out")
n=$(segment_count "$store")
[ "$n" -ge "$SEGMENTS" ] || die "$store holds $n segments, not $SEGMENTS: run load.sh first"

if [ -f "$out/server.pid" ]; then
  kill "$(cat "$out/server.pid")" 2>/dev/null || true
  sleep 2
fi
addr=localhost:50052
[ "$(uname)" = Linux ] && addr=:50052
write_tenants "$out/tenants-query.json" "$store"
cmd=("$bin/cloudy" query -tenants-file="$out/tenants-query.json" -distance="$v" -addr="$addr"
  -pprof-addr=localhost:6060)
[ -n "$cpu" ] && cmd=(taskset -c "$cpu" "${cmd[@]}")

log "$m: start the $v server on cpu '${cpu:-any}'"
t0=$(now)
GOMAXPROCS=1 nohup "${cmd[@]}" >"$d/server.log" 2>&1 </dev/null &
pid=$!
echo "$pid" >"$out/server.pid"
synced() {
  awk -v key="manifest=ns/$NAMESPACE/refs/heads/main.json" '/msg="sync pass"/ && index($0, key " ") { print; exit }' "$d/server.log"
}
deadline=$(($(date +%s) + 1800))
until [ -n "$(synced)" ]; do
  kill -0 "$pid" 2>/dev/null || die "server died: $d/server.log"
  [ "$(date +%s)" -lt "$deadline" ] || die "no sync pass after 30 minutes"
  sleep 1
done
t1=$(now)
{
  echo "cold_start_wall_s $(awk -v a="$t0" -v b="$t1" 'BEGIN {printf "%.1f", b - a}')"
  synced
  echo "server_cpu ${cpu:-any}"
  echo "client_cpus ${ccpu:-any}"
} >"$d/server.txt"
sleep 5
echo "rss_kb_after_sync $(ps -o rss= -p "$pid" | tr -d ' ')" >>"$d/server.txt"

qb=$(built //scripts:querybench)
scenarios "$qb" localhost:50052 "$d/$client" "$ccpu"

log "$m: profile the $v server"
marker=$d/profile-run.jsonl
pinned "$ccpu" "$qb" --target=localhost:50052 --tenant="$TENANT" --namespace="$NAMESPACE" --data_dir="$DATA_DIR" --queries=1000 --warmup=0 \
  --top_k=10 --out="$marker" >"$d/profile-run.log" 2>&1 &
sleep 5
curl -fsS --max-time 60 -o "$d/cpu-server.pprof" "http://localhost:6060/debug/pprof/profile?seconds=30" ||
  log "$m: profile capture failed"
pkill -f -- "$marker" || true
rm -f "$marker"
echo "rss_kb_after_local $(ps -o rss= -p "$pid" | tr -d ' ')" >>"$d/server.txt"
