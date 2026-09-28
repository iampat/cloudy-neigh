"""Latency and correctness per machine, variant, client and scenario, from the querybench JSONL."""

import glob
import json
import os

import numpy as np
import pyarrow.parquet as pq

from absl import app, flags

FLAGS = flags.FLAGS
flags.DEFINE_string("run_dir", None, "Run directory with the benchmark and e2e results")
flags.DEFINE_string("groundtruth", None, "Ground-truth JSONL from groundtruth.py")
flags.DEFINE_string("data_dir", None, "Directory of the Cohere Wikipedia Parquet files")
flags.DEFINE_string("out", None, "Output file")
flags.mark_flags_as_required(["run_dir", "groundtruth", "data_dir", "out"])


def pct(xs: list[float]) -> dict[str, float]:
    a = np.array(xs) * 1000
    return {
        "mean": float(a.mean()),
        "p50": float(np.percentile(a, 50)),
        "p90": float(np.percentile(a, 90)),
        "p99": float(np.percentile(a, 99)),
        "max": float(a.max()),
    }


def en_ids(data_dir: str) -> set[str]:
    ids: set[str] = set()
    for path in glob.glob(f"{data_dir}/en/*.parquet"):
        col = pq.read_table(path, columns=["_id"]).column("_id")
        ids.update(str(x) for x in col.to_pylist())
    return ids


def analyze(path: str, truth: dict[int, dict], english: set[str]) -> dict | None:
    machine, variant, client = path.split(os.sep)[-4:-1]
    scenario = os.path.basename(path).removesuffix(".jsonl")
    k = int(scenario.split("-")[0].removeprefix("top"))
    filt = "en" if scenario.endswith("-en") else "all"
    with open(path) as f:
        res = [json.loads(line) for line in f]
    if not res:
        return None
    recalls, top1, score_err, bad_lang, short = [], 0, 0.0, 0, 0
    for r in res:
        t = truth[r["query"]][filt]
        recalls.append(len(set(r["ids"]) & set(t["ids"][:k])) / k)
        top1 += r["ids"][:1] == t["ids"][:1]
        if r["scores"]:
            err = max(abs(a - (1 - b)) for a, b in zip(r["scores"], t["scores"][:k]))
            score_err = max(score_err, err)
        bad_lang += filt == "en" and any(i not in english for i in r["ids"])
        short += len(r["ids"]) != k
    return {
        "machine": machine,
        "variant": variant,
        "client": client,
        "scenario": scenario,
        "n": len(res),
        "total": pct([r["total_s"] for r in res]),
        "server": pct([r["server_s"] for r in res]),
        "client_server": pct([r["total_s"] - r["server_s"] for r in res]),
        "recall": float(np.mean(recalls)),
        "min_recall": float(np.min(recalls)),
        "perfect_queries": int(sum(x == 1.0 for x in recalls)),
        "top1_match": top1 / len(res),
        "max_score_err": score_err,
        "bad_lang_queries": int(bad_lang),
        "short_results": int(short),
    }


def main(argv: list[str]) -> None:
    del argv  # Unused.

    truth = {}
    with open(FLAGS.groundtruth) as f:
        for line in f:
            r = json.loads(line)
            truth[r["query"]] = r
    english = en_ids(FLAGS.data_dir)
    rows = []
    for path in sorted(glob.glob(f"{FLAGS.run_dir}/e2e/*/*/*/top*.jsonl")):
        row = analyze(path, truth, english)
        if row:
            rows.append(row)
            print(
                f"{row['machine']:4} {row['variant']:13} {row['client']:4} "
                f"{row['scenario']:11} n={row['n']} "
                f"total p50={row['total']['p50']:.1f} server p50={row['server']['p50']:.1f} "
                f"recall={row['recall']:.4f}"
            )
    with open(FLAGS.out, "w") as f:
        json.dump(rows, f, indent=1)


if __name__ == "__main__":
    app.run(main)
