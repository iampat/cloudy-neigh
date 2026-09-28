"""Brute-force top-k over all normalized vectors for the first query vectors of en/0000."""

import glob
import json
import os

import numpy as np
import pyarrow.parquet as pq

from absl import app, flags

FLAGS = flags.FLAGS
flags.DEFINE_string("data_dir", None, "Directory of the Cohere Wikipedia Parquet files")
flags.DEFINE_string("out", None, "Output file")
flags.DEFINE_integer("queries", 1000, "Number of query vectors from en/0000.parquet")
flags.DEFINE_integer("k", 100, "Neighbors per query")
flags.mark_flags_as_required(["data_dir", "out"])


def load_corpus(data_dir: str) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    ids, langs, vecs = [], [], []
    for path in sorted(glob.glob(f"{data_dir}/*/*.parquet")):
        lang = os.path.basename(os.path.dirname(path))
        table = pq.read_table(path, columns=["_id", "emb"])
        ids += [str(x) for x in table.column("_id").to_pylist()]
        langs += [lang] * table.num_rows
        emb = table.column("emb").to_numpy(zero_copy_only=False)
        vecs.append(np.stack(emb).astype(np.float32))
    x = np.concatenate(vecs)
    x /= np.linalg.norm(x, axis=1, keepdims=True)
    return np.array(ids), np.array(langs) == "en", x


def load_queries(data_dir: str, n: int) -> np.ndarray:
    rg = pq.ParquetFile(f"{data_dir}/en/0000.parquet").read_row_group(
        0, columns=["emb"]
    )
    q = np.stack(rg.column("emb").to_numpy(zero_copy_only=False)[:n]).astype(np.float32)
    q /= np.linalg.norm(q, axis=1, keepdims=True)
    return q


def top(row: np.ndarray, k: int) -> np.ndarray:
    idx = np.argpartition(-row, k)[:k]
    return idx[np.argsort(-row[idx])]


def main(argv: list[str]) -> None:
    del argv  # Unused.

    ids, en, x = load_corpus(FLAGS.data_dir)
    print(f"docs {len(ids)}, unique ids {len(set(ids))}")
    q = load_queries(FLAGS.data_dir, FLAGS.queries)
    tmp = FLAGS.out + ".tmp"
    with open(tmp, "w") as f:
        for start in range(0, len(q), 50):
            scores = q[start : start + 50] @ x.T
            scores_en = np.where(en, scores, -np.inf)
            for j in range(scores.shape[0]):
                rec = {"query": start + j}
                for name, row in (("all", scores[j]), ("en", scores_en[j])):
                    t = top(row, FLAGS.k)
                    rec[name] = {"ids": ids[t].tolist(), "scores": row[t].tolist()}
                f.write(json.dumps(rec) + "\n")
    os.replace(tmp, FLAGS.out)


if __name__ == "__main__":
    app.run(main)
