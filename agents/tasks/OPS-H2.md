# OPS-H2: Unify Python dependencies into one `requirements.txt`

| | |
|---|---|
| **Owner** | backend-engineer |
| **Priority** | P1 |
| **PRD ref** | Technical PRD §11 "Unify Python deps" |
| **Depends on** | OPS-H1 (same Dockerfiles; avoids merge conflicts) |
| **Status** | Ready |

## Goal
Setup currently needs `pip install -r requirements-m0.txt minio ulid-py fastapi uvicorn`, repeated in README, both
Dockerfiles, and `services/AGENTS.md`. One file, one command.

## Scope
- New `requirements.txt` with every runtime dep (lower-bound pins as today); `requirements-dev.txt` with `pytest` and dev-only tools, `-r requirements.txt` at the top.
- Keep `requirements-m0.txt` as a one-line `-r requirements.txt` shim for one release, with a comment, so old instructions still work.
- Update both Dockerfiles, README Quick start, `services/AGENTS.md` Setup, Makefile `setup-py`, CI workflow.
- CI: install CPU-only torch (`--extra-index-url https://download.pytorch.org/whl/cpu`) to keep CI fast.

## Acceptance criteria
- [ ] AC1 — Fresh venv: `pip install -r requirements-dev.txt && pytest tests/test_m0.py` passes.
- [ ] AC2 — No remaining `pip install ... minio ulid-py fastapi uvicorn` strings in the repo (`grep -rn "ulid-py fastapi"` empty, except the shim comment).
- [ ] AC3 — Both Docker images build; `make stack-up` still healthy.
- [ ] AC4 — CI green.

## Verification
```bash
python3 -m venv /tmp/v && /tmp/v/bin/pip install -r requirements-dev.txt && PYTHONPATH=$PWD /tmp/v/bin/pytest tests/test_m0.py
docker compose --profile workers build
grep -rn "ulid-py fastapi" . --exclude-dir=.git
```

## Notes
