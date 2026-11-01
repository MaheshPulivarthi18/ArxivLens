# Progress log

Append-only. Newest entry first. One entry per task per cycle. Agents read the top 20.

Format:
```
## YYYY-MM-DD — <TASK-ID> — <outcome: shipped | blocked | in review | changes requested>
- Agent: <role> · Attempts: <n> · Review rounds: <n> · PR: <link or —>
- Summary: <one or two lines>
- Blockers / follow-ups: <or —>
```

---

## 2026-11-01 — SETUP — shipped
- Agent: human + setup · Attempts: 1 · Review rounds: 0 · PR: (this PR)
- Summary: Added agent team (4 roles), memory layout (`agents/`), ADRs 0001–0005 as Proposed, initial backlog of 10 tasks, Makefile harness, CI.
- Blockers / follow-ups: Human to decide ADR-0001..0005. QA-8 waits on ADR-0001/0002.
