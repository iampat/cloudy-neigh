"""Writes report.md for one run directory from summary.json and the raw benchmark files."""

import glob
import json
import os
import re
import statistics
from collections import defaultdict

from absl import app, flags

FLAGS = flags.FLAGS
flags.DEFINE_string("run_dir", None, "Run directory with the benchmark and e2e results")
flags.DEFINE_string("summary", None, "summary.json from analyze.py")
flags.DEFINE_string("out", None, "Output file")
flags.mark_flags_as_required(["run_dir", "summary", "out"])

VARIANTS = ["pure", "simd", "fp16"]
BENCH_FILES = ["distance", "decode16", "scankernel", "search", "scan1m"]
CACHED = {"pure": "pure", "simd": "simd-arch"}
BENCH_LINE = re.compile(
    r"^(Benchmark\S+?)(-\d+)?\s+\d+\s+([\d.]+) ns/op(?:\s+([\d.]+) ns/row)?"
)


def f(x: float | None, d: int = 1) -> str:
    return "-" if x is None else f"{x:.{d}f}"


def table(head: list[str], rows: list[list[str]], right_from: int = 1) -> str:
    align = ["---" if i < right_from else "---:" for i in range(len(head))]
    lines = ["| " + " | ".join(head) + " |", "| " + " | ".join(align) + " |"]
    lines += ["| " + " | ".join(r) + " |" for r in rows]
    return "\n".join(lines) + "\n"


def keyvals(path: str, pattern: str = r"^([a-z_]+)\s+(.+)$") -> dict[str, str]:
    out: dict[str, str] = {}
    if not os.path.exists(path):
        return out
    with open(path) as fh:
        for line in fh:
            m = re.match(pattern, line.rstrip())
            if m and m.group(1) not in out:
                out[m.group(1)] = m.group(2).strip()
    return out


def machines(run: str) -> list[str]:
    found = {
        os.path.basename(os.path.dirname(p)) for p in glob.glob(f"{run}/*/bench-*.txt")
    }
    found |= {os.path.basename(p) for p in glob.glob(f"{run}/e2e/*")}
    order = ["mac", "emr", "gnr"]

    def key(m: str) -> tuple[int, str]:
        base = m.split("-")[0]
        return (order.index(base) if base in order else 99, m)

    return sorted(found, key=key)


def medians(paths: list[str]) -> tuple[dict[str, float], dict[str, float]]:
    ops: dict[str, list[float]] = defaultdict(list)
    rows: dict[str, list[float]] = defaultdict(list)
    for p in paths:
        if not os.path.exists(p):
            continue
        with open(p) as fh:
            for line in fh:
                m = BENCH_LINE.match(line)
                if m:
                    ops[m.group(1)].append(float(m.group(3)))
                    if m.group(4):
                        rows[m.group(1)].append(float(m.group(4)))
    return (
        {k: statistics.median(v) for k, v in ops.items()},
        {k: statistics.median(v) for k, v in rows.items()},
    )


def gb(kb: str | None) -> str:
    return "-" if not kb or not kb.isdigit() else f"{int(kb) / 1024 / 1024:.1f}"


def section_machines(run: str, ms: list[str]) -> str:
    rows = []
    seen = set()
    for m in ms:
        base = m.split("-")[0]
        if base in seen:
            continue
        seen.add(base)
        if base == "mac":
            cpu = (
                open(f"{run}/mac/cpu.txt").read().split("\n")
                if os.path.exists(f"{run}/mac/cpu.txt")
                else []
            )
            name = cpu[0] if cpu else "-"
            vcpu = cpu[1] if len(cpu) > 1 else "-"
            rows.append(["mac", name, vcpu, "local", "-"])
            continue
        lscpu = keyvals(f"{run}/{base}/lscpu.txt", r"^\s*([^:]+):\s+(.+)$")
        vm = keyvals(f"{run}/{base}/vm.txt")
        model = lscpu.get("Model name", "-")
        if "Model" in lscpu:
            model += f" (model {lscpu['Model']})"
        rows.append(
            [
                base,
                model,
                lscpu.get("CPU(s)", "-"),
                vm.get("shape", "-"),
                vm.get("zone", "-"),
            ]
        )
    out = "## Machines\n\n" + table(
        ["Machine", "CPU", "vCPUs", "Shape", "Zone"], rows, 5
    )
    loc = f"{run}/bucket-location.txt"
    if os.path.exists(loc):
        out += f"\nBucket location: `{open(loc).read().strip()}`.\n"
    return out


def section_load(run: str, ms: list[str]) -> str:
    rows = []
    for m in ms:
        kv = keyvals(f"{run}/{m}/load.txt")
        if not kv:
            continue
        size = kv.get("store_bytes")
        size = f"{int(size) / 2**30:.1f}" if size and size.isdigit() else "-"
        rows.append(
            [
                m,
                kv.get("store", "-"),
                kv.get("demoload_s", "-"),
                kv.get("flushed_s", "-"),
                kv.get("segments", "-"),
                size,
            ]
        )
    if not rows:
        return ""
    head = ["Machine", "Store", "Demoload s", "Flushed s", "Segments", "Size GiB"]
    return "## Load\n\n" + table(head, rows, 2)


def section_server(run: str, ms: list[str]) -> str:
    rows = []
    for m in ms:
        for path in sorted(glob.glob(f"{run}/e2e/{m}/*/server.txt")):
            v = os.path.basename(os.path.dirname(path))
            kv = keyvals(path)
            sync = "-"
            with open(path) as fh:
                hit = re.search(r"total_dur=(\S+)", fh.read())
                if hit:
                    sync = hit.group(1)
            cold = kv.get("cold_start_wall_s")
            rss_query = (
                kv.get("rss_kb_after_vm")
                or kv.get("rss_kb_after_local")
                or kv.get("rss_kb_after_mac")
            )
            rows.append(
                [
                    m,
                    f"`{v}`",
                    f(float(cold)) if cold else "-",
                    sync,
                    gb(kv.get("rss_kb_after_sync")),
                    gb(rss_query),
                    kv.get("server_cpu", kv.get("psr", "-")),
                ]
            )
    if not rows:
        return ""
    head = [
        "Machine",
        "Variant",
        "Cold start s",
        "First sync",
        "RSS sync GiB",
        "RSS query GiB",
        "CPU",
    ]
    return (
        "## Server\n\nCold start runs from process start to the first `sync pass`.\n\n"
        + table(head, rows, 2)
    )


def section_summary(summ: list[dict]) -> str:
    top10 = [r for r in summ if r["scenario"] == "top10-all"]
    if not top10:
        return ""
    order = ["emr", "mac", "gnr"]

    def key(r: dict) -> tuple[int, str, int]:
        m = r["machine"]
        base = m.split("-")[0]
        base_idx = order.index(base) if base in order else 99
        v_idx = VARIANTS.index(r["variant"]) if r["variant"] in VARIANTS else 99
        return (base_idx, m, v_idx)

    rows = [
        [
            r["machine"],
            f"`{r['variant']}`",
            r["client"],
            f(r["total"]["p50"]),
            f(r["total"]["p99"]),
            f(r["server"]["p50"]),
            f(r["server"]["p99"]),
            f(r["client_server"]["p50"]),
            f(r["client_server"]["p99"]),
            f(r["recall"], 4),
        ]
        for r in sorted(top10, key=key)
    ]
    head = [
        "Machine",
        "Variant",
        "Client",
        "Total p50",
        "Total p99",
        "Server p50",
        "Server p99",
        "C-S p50",
        "C-S p99",
        "Recall@k",
    ]
    return (
        "## Summary (top10-all)\n\n"
        "All values in ms. 1M documents, 300 queries, localhost client-server.\n\n"
        + table(head, rows, 3)
    )


def server_p50(summ: list[dict], m: str, v: str) -> float | None:
    target_machines = [f"{m}-local", m, f"{m}-gcp"]
    for tm in target_machines:
        for r in summ:
            if (r["machine"], r["variant"], r["scenario"]) == (tm, v, "top10-all"):
                return r["server"]["p50"]
    return None


def server_p99(summ: list[dict], m: str, v: str) -> float | None:
    target_machines = [f"{m}-local", m, f"{m}-gcp"]
    for tm in target_machines:
        for r in summ:
            if (r["machine"], r["variant"], r["scenario"]) == (tm, v, "top10-all"):
                return r["server"]["p99"]
    return None


def section_kernels(run: str, m: str, summ: list[dict]) -> str:
    base = m.split("-")[0]
    ops, rows = medians([f"{run}/{base}/bench-{base}-{x}.txt" for x in BENCH_FILES])
    if not ops:
        return ""
    out = f"### {m}: per-row split, ns, D=1024, 1M rows\n\n"
    split = []
    for v in VARIANTS:
        cached = ops.get(f"BenchmarkDistance/DotProduct/{CACHED.get(v, v)}/1024")
        stream = rows.get(f"BenchmarkScanKernel/{v}/1M")
        scan = ops.get(f"BenchmarkScan1M_Table/{v}")
        if stream is None and scan is None:
            continue
        scan = scan / 1e6 if scan else None
        srv = server_p50(summ, m, v)
        srv99 = server_p99(summ, m, v)
        k10 = ops.get(f"BenchmarkSearch10K1024_Table/{v}")
        k256 = ops.get(f"BenchmarkSearch256x1024_Table/{v}")
        split.append(
            [
                f"`{v}`",
                f(cached),
                f(stream),
                f(scan),
                f(srv),
                f(srv99),
                f(stream - cached if stream and cached else None),
                f(scan - stream if scan and stream else None),
                f(srv - scan if srv and scan else None),
                f(k10 / 1e4 if k10 else None),
                f(k256 / 256 if k256 else None),
            ]
        )
    head = [
        "Variant",
        "Cached kernel",
        "Streaming kernel",
        "Scan1M",
        "Server p50",
        "Server p99",
        "DRAM",
        "Scan overhead",
        "Server minus Scan1M",
        "10K Search",
        "256 Search",
    ]
    out += table(head, split)
    out += (
        "\nServer p50 and p99 are the top10-all server times in ms, which equal "
        "ns per row over 1M rows.\n"
    )

    out += f"\n### {m}: float32 kernels, median ns/op\n\n"
    kr = []
    for k in ["DotProduct", "L2Squared", "Cosine", "NormalizeInPlace"]:
        for d in [128, 1024, 4096]:
            vals = [
                ops.get(f"BenchmarkDistance/{k}/{c}/{d}")
                for c in ["pure", "simd-portable", "simd-arch"]
            ]
            if any(vals):
                kr.append([k, str(d)] + [f(x) for x in vals])
    out += table(["Kernel", "D", "pure", "simd-portable", "simd-arch"], kr, 1)

    v16 = [v for v in VARIANTS if "16" in v]
    kr = []
    for k in ["DotProduct", "L2Squared"]:
        for d in [128, 1024, 4096]:
            vals = [ops.get(f"BenchmarkDistance/{k}/{v}/{d}") for v in v16]
            if any(vals):
                kr.append([k, str(d)] + [f(x) for x in vals])
    if kr:
        out += f"\n### {m}: 16-bit kernels, median ns/op\n\n" + table(
            ["Kernel", "D"] + v16, kr, 1
        )
    fp = ops.get("BenchmarkDecode16")
    if fp:
        out += f"\nDecode16 at D=1024: fp16 {f(fp)} ns.\n"
    return out


def section_e2e(summ: list[dict], ms: list[str]) -> str:
    out = "## End-to-end latency\n\nAll values in ms. Client-server is total minus server time.\n"
    for m in ms:
        rs = [r for r in summ if r["machine"] == m]
        if not rs:
            continue
        rows = [
            [
                f"`{r['variant']}`",
                r["client"],
                r["scenario"],
                str(r["n"]),
                f(r["total"]["p50"]),
                f(r["total"]["p99"]),
                f(r["server"]["p50"]),
                f(r["server"]["p99"]),
                f(r["client_server"]["p50"]),
                f(r["client_server"]["p99"]),
            ]
            for r in rs
        ]
        head = [
            "Variant",
            "Client",
            "Scenario",
            "n",
            "Total p50",
            "Total p99",
            "Server p50",
            "Server p99",
            "C-S p50",
            "C-S p99",
        ]
        out += f"\n### {m}\n\n" + table(head, rows, 3)
    return out


def section_recall(summ: list[dict]) -> str:
    if not summ:
        return ""
    rows = [
        [
            r["machine"],
            f"`{r['variant']}`",
            r["client"],
            r["scenario"],
            f(r["recall"], 4),
            f(r["min_recall"], 2),
            f(r["top1_match"], 3),
            f"{r['max_score_err']:.1e}",
            str(r["bad_lang_queries"]),
            str(r["short_results"]),
        ]
        for r in summ
    ]
    head = [
        "Machine",
        "Variant",
        "Client",
        "Scenario",
        "Recall@k",
        "Min recall",
        "Top-1",
        "Max score error",
        "Wrong lang",
        "Short",
    ]
    note = "Exact variants show recall less than 1 only on float32 ties.\n\n"
    return "## Correctness\n\n" + note + table(head, rows, 4)


def section_profiles(run: str) -> str:
    tops = sorted(glob.glob(f"{run}/**/*.top.txt", recursive=True))
    if not tops:
        return ""
    out = "## Profiles\n\nThe top 12 nodes of each `pprof -top`.\n"
    for path in tops:
        with open(path) as fh:
            lines = fh.read().splitlines()
        start = next(
            (i for i, x in enumerate(lines) if x.strip().startswith("flat")), 0
        )
        excerpt = "\n".join(lines[start : start + 13])
        out += f"\n### `{os.path.relpath(path, run)}`\n\n```\n{excerpt}\n```\n"
    return out


def section_sanity(run: str) -> str:
    path = f"{run}/sanity/sanity.txt"
    if not os.path.exists(path):
        return ""
    with open(path) as fh:
        content = fh.read().strip()
    return f"## Format Compatibility Sanity Check\n\n{content}\n"


def main(argv: list[str]) -> None:
    del argv  # Unused.
    run = FLAGS.run_dir.rstrip("/")

    with open(FLAGS.summary) as fh:
        summ = json.load(fh)
    ms = machines(run)
    build = keyvals(f"{run}/build.txt")
    parts = [f"# E2E benchmark: {os.path.basename(run)}\n"]
    if build:
        parts.append(
            f"Commit `{build.get('commit', '-')}` on `{build.get('branch', '-')}`, built {build.get('built', '-')}.\n"
        )
    parts.append(section_machines(run, ms))
    parts.append(section_summary(summ))
    parts.append(section_load(run, ms))
    parts.append(section_server(run, ms))
    seen_bases = set()
    kernels = []
    for m in ms:
        b = m.split("-")[0]
        if b not in seen_bases:
            seen_bases.add(b)
            k = section_kernels(run, b, summ)
            if k:
                kernels.append(k)
    if kernels:
        parts.append("## Kernels\n\n" + "\n".join(kernels))
    parts.append(section_e2e(summ, ms))
    parts.append(section_recall(summ))
    parts.append(section_sanity(run))
    parts.append(section_profiles(run))
    with open(FLAGS.out, "w") as fh:
        fh.write("\n".join(p for p in parts if p))


if __name__ == "__main__":
    app.run(main)
