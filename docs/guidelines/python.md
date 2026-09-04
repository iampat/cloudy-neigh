# Python guidelines

These rules bind all Python code in this repository. Read them before the first
Python edit in a task. Review Python changes against them.

## Tooling and environment

- Always use a virtual environment (`.venv`). Never install packages globally.
- Use Astral tooling: `uv` for package management and `ruff` for formatting and linting.
- Format with `ruff format`. Check with `ruff check`.

## Dependencies

- Dependencies must be pinned to an exact version (`package==x.y.z`).
- Never use ranges (`>=`, `~=`) or unpinned package names.
- List dependencies in `requirements.txt`. Do not use inline script metadata.

## Style

- Follow the Google Python Style Guide.
- Use modern type annotations (`int | None`, `list[str]`).
- Do not add shebang lines (`#!/usr/bin/env python3`).
