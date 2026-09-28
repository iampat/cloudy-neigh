# Runs on the Mac or a VM. Usage: host/bench.sh <out dir> <machine>
set -euo pipefail
. "$(dirname "$0")/../lib.sh"
out=$1 m=$2
mkdir -p "$out" && out=$(cd "$out" && pwd)
d=$out/$m
bin=$(bin_dir "$out")
mkdir -p "$d"
read -r cpu _ <<<"$(pin_layout)"
cd "$bin"

"$bin/vector_test" -test.run=. -test.count=1 >"$d/tests.txt" 2>&1 || die "vector tests failed: $d/tests.txt"
"$bin/query_test" -test.run=. -test.count=1 >>"$d/tests.txt" 2>&1 || die "query tests failed: $d/tests.txt"
log "$m: tests pass, benchmarks on cpu '${cpu:-any}'"

export GOMAXPROCS=1
b() {
  local name=$1 bin=$2 pattern=$3
  shift 3
  log "$m: $name"
  pinned "$cpu" "$bin" -test.run='^$' -test.cpu=1 -test.count=5 -test.bench="$pattern" "$@" \
    >"$d/bench-$m-$name.txt" 2>&1 || die "$name failed: $d/bench-$m-$name.txt"
}
b distance "$bin/vector_test" '^BenchmarkDistance$' -test.benchtime=200ms
b decode16 "$bin/vector_test" '^BenchmarkDecode16$' -test.benchtime=200ms
b scankernel "$bin/vector_test" '^BenchmarkScanKernel$'
b search "$bin/query_test" '^BenchmarkSearch(10K1024|256x1024)_Table$'
b scan1m "$bin/query_test" '^BenchmarkScan1M_Table$'

for v in $(var PROFILE "$m"); do
  log "$m: profile Scan1M $v"
  pinned "$cpu" "$bin/query_test" -test.run='^$' -test.cpu=1 -test.count=1 -test.benchtime=10s \
    -test.bench="^BenchmarkScan1M_Table/$v\$" -test.cpuprofile="$d/cpu-scan1m-$m-$v.pprof" \
    >"$d/bench-$m-scan1m-prof-$v.txt" 2>&1 || die "profile $v failed"
done
