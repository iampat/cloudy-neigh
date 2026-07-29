# cloudy-neigh — Project Rules & Guidelines

## 1. Build System & Tooling
- **Build System:** Bazel is the primary build system (`bazel test //...`, `bazel build //...`).
- **Gazelle Integration:** Use Gazelle to manage Go build rules (`bazel run //:gazelle`).
- **Python Execution:** Do not run Python scripts directly; execute them via `bazel run`.

## 2. Documentation & Design Standards
- **HTML Design Docs:** Documentation (e.g. design notes in `docs/design/`) must prioritize simplicity, clean layout, readability, and consistency. Avoid over-complicating docs with heavy CSS or JS frameworks.
- **Design Specifications:** Keep architecture and task specifications updated alongside code changes.

## 3. Engineering & Logging Standards
- **Structured Logging:** Use Go standard `log/slog` for operational events, performance metrics, CAS retries, and batching logs.
- **Testing:** Ensure all package changes include unit/integration tests verified with `bazel test //...`.
