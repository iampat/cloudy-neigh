# Runs on the Mac or a VM. Usage: host/load.sh <out dir> <machine> <store url>
set -euo pipefail
. "$(dirname "$0")/../lib.sh"
out=$1 m=$2 store=$3
mkdir -p "$out" && out=$(cd "$out" && pwd)
d=$out/$m
mkdir -p "$d"
n=$(segment_count "$store")
if [ "$n" -ge "$SEGMENTS" ]; then
  log "$m: $store holds $n segments from an earlier run: skip the upload"
  bytes=$(store_bytes "$store")
  {
    echo "reused_load 1"
    echo "segments $n"
    echo "store $store"
    echo "store_bytes $bytes"
  } >"$d/load.txt"
  exit 0
fi
[ "$n" -eq 0 ] || die "$store holds $n of $SEGMENTS segments from an incomplete load: delete ns/$NAMESPACE first"

write_tenants "$d/tenants-ingest.json" "$store"
"$(bin_dir "$out")/cloudy" ingest -tenants-file="$d/tenants-ingest.json" -addr=localhost:50051 \
  >"$d/ingest.log" 2>&1 </dev/null &
ingest=$!
trap 'kill "$ingest" 2>/dev/null || true' EXIT
sleep 3
kill -0 "$ingest" || die "ingest server died: $d/ingest.log"

log "$m: demoload into $store"
t0=$(date +%s)
demoload=$(built //scripts:demoload)
"$demoload" --tenant="$TENANT" --namespace="$NAMESPACE" --batch_size="$BATCH_SIZE" --data_dir="$DATA_DIR" \
  >"$d/demoload.log" 2>&1 </dev/null || die "demoload failed: $d/demoload.log"
echo "demoload_s $(($(date +%s) - t0))" >"$d/load.txt"

log "$m: wait for $SEGMENTS segments"
deadline=$(($(date +%s) + 3600))
while true; do
  n=$(segment_count "$store")
  [ "$n" -lt "$SEGMENTS" ] || break
  [ "$(date +%s)" -lt "$deadline" ] || die "fewer than $SEGMENTS segments after one hour"
  kill -0 "$ingest" || die "ingest server died: $d/ingest.log"
  sleep 10
done
flushed_s=$(($(date +%s) - t0))
sleep 15
n=$(segment_count "$store")
bytes=$(store_bytes "$store")
{
  echo "flushed_s $flushed_s"
  echo "segments $n"
  echo "store $store"
  echo "store_bytes $bytes"
} >>"$d/load.txt"
