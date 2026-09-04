"""Dataset loader for cloudy-neigh customer demo."""

import argparse
import glob
import logging
import os
import sys
import time
from typing import Any

import pyarrow.parquet as pq

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
logger = logging.getLogger("demoload")


def send_batch(target: str, batch: list[dict[str, Any]]) -> None:
    """Send a batch of documents to the cloudy-neigh ingestion service.

    Empty stub for now. Will be wired to gRPC/REST Write API once available.
    """
    del target, batch


def parse_lang_from_path(file_path: str) -> str:
    """Extract language code from the file path structure (e.g. datasets/en/0000.parquet)."""
    parent = os.path.basename(os.path.dirname(os.path.abspath(file_path)))
    if len(parent) in (2, 3) and parent.isalpha():
        return parent
    return "en"


def load_dataset(
    data_dir: str,
    batch_size: int = 1000,
    max_docs: int | None = None,
    target: str = "localhost:50051",
) -> None:
    """Read Parquet files from data_dir and stream batches to the target service."""
    pattern = os.path.join(data_dir, "**", "*.parquet")
    files = sorted(glob.glob(pattern, recursive=True))

    if not files:
        logger.error("No Parquet files found in %s", data_dir)
        sys.exit(1)

    logger.info("Found %d Parquet file(s) in %s", len(files), data_dir)
    total_docs = 0
    total_batches = 0
    start_time = time.time()

    for file_path in files:
        lang = parse_lang_from_path(file_path)
        logger.info("Reading %s (lang=%s)...", file_path, lang)
        pf = pq.ParquetFile(file_path)

        for batch in pf.iter_batches(batch_size=batch_size):
            pydict = batch.to_pydict()
            n = len(pydict["_id"])
            rows: list[dict[str, Any]] = []

            for i in range(n):
                rows.append(
                    {
                        "id": pydict["_id"][i],
                        "url": pydict["url"][i],
                        "title": pydict["title"][i],
                        "text": pydict["text"][i],
                        "lang": lang,
                        "vector": pydict["emb"][i],
                    }
                )

            send_batch(target, rows)
            total_docs += n
            total_batches += 1

            if total_batches % 10 == 0:
                elapsed = time.time() - start_time
                rate = total_docs / elapsed if elapsed > 0 else 0.0
                logger.info(
                    "Streamed %d docs (%d batches) [%.0f docs/s]",
                    total_docs,
                    total_batches,
                    rate,
                )

            if max_docs is not None and total_docs >= max_docs:
                logger.info("Reached limit of %d documents", max_docs)
                break

        if max_docs is not None and total_docs >= max_docs:
            break

    elapsed = time.time() - start_time
    rate = total_docs / elapsed if elapsed > 0 else 0.0
    logger.info(
        "Finished streaming %d documents in %.2fs (%.0f docs/s)",
        total_docs,
        elapsed,
        rate,
    )


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Stream Wikipedia dataset into cloudy-neigh"
    )
    parser.add_argument(
        "--data-dir",
        default="datasets/cohere-wikipedia",
        help="Directory containing Parquet files (default: datasets/cohere-wikipedia)",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=1000,
        help="Batch size for writes (default: 1000)",
    )
    parser.add_argument(
        "--max-docs",
        type=int,
        default=None,
        help="Maximum documents to stream (default: all)",
    )
    parser.add_argument(
        "--target",
        default="localhost:50051",
        help="Target ingest address (default: localhost:50051)",
    )
    args = parser.parse_args()

    load_dataset(
        data_dir=args.data_dir,
        batch_size=args.batch_size,
        max_docs=args.max_docs,
        target=args.target,
    )


if __name__ == "__main__":
    main()
