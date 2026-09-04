# cloudy-neigh task runner

# Download 1M Wikipedia documents (10 Parquet files, ~2.1 GB) to datasets/cohere-wikipedia/
download-dataset:
	hf download CohereLabs/wikipedia-2023-11-embed-multilingual-v3 \
		en/0000.parquet en/0001.parquet en/0002.parquet en/0003.parquet \
		en/0004.parquet en/0005.parquet en/0006.parquet \
		de/0000.parquet fr/0000.parquet es/0000.parquet \
		--repo-type dataset --local-dir datasets/cohere-wikipedia

# Download 100k English documents sample (~216 MB) to datasets/cohere-wikipedia/
download-dataset-sample:
	hf download CohereLabs/wikipedia-2023-11-embed-multilingual-v3 \
		en/0000.parquet \
		--repo-type dataset --local-dir datasets/cohere-wikipedia

# Stream dataset into cloudy-neigh
load-dataset *args:
	uv run python scripts/demoload.py {{args}}

# Set up Python environment and install script requirements
setup-python:
	uv venv --allow-existing .venv
	uv pip install -r scripts/requirements.txt



