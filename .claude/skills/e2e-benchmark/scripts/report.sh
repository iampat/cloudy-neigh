# Usage: report.sh [run dir] [out dir]
# Builds the ground truth once, writes pprof -top files, then summary.json and report.md.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
run=${1:-$RUN_DIR}
out=${2:-$run}
mkdir -p "$out"
run=$(cd "$run" && pwd)
out=$(cd "$out" && pwd)
tool() { "$(built "//scripts/e2e:$1")" "${@:2}"; }

if [ ! -f "$GROUNDTRUTH" ]; then
  log "ground truth: brute force over the full corpus"
  tool groundtruth --data_dir="$DATA_DIR" --out="$GROUNDTRUTH"
fi
find "$run" -name '*.pprof' | while read -r p; do
  top=${p%.pprof}.top.txt
  [ -f "$top" ] || (cd "$REPO" && bazel run @rules_go//go -- tool pprof -top -nodecount=25 "$p" >"$top" 2>/dev/null) ||
    log "pprof failed on $p"
done
tool analyze --run_dir="$run" --groundtruth="$GROUNDTRUTH" --data_dir="$DATA_DIR" \
  --out="$out/summary.json"
tool report --run_dir="$run" --summary="$out/summary.json" --out="$out/report.md"
log "wrote $out/report.md"
