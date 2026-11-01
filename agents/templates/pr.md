## <TASK-ID>: <title>

**Agent:** <role> · **Spec:** `agents/tasks/<TASK-ID>.md` · **PRD ref:** <id>

### What changed
- 

### Acceptance criteria
- [x] AC1 — evidence (test name / command output / screenshot)
- [x] AC2 — evidence

### Verification
| Command | Result |
|---|---|
| `make test-unit` | pass / fail |
| `make lint` | pass / fail |
| `make eval` (if retrieval/answering touched) | before → after, or "not run: <reason>" |

### Not verified / risks
- 

### Follow-ups created
- 

### Evidence (filled by pm-lead when raising the PR)
| AC | Evidence | |
|---|---|---|
| AC1 | `agents/reviews/<TASK-ID>/evidence/01-….png` | ![](https://github.com/MaheshPulivarthi18/ArxivLens/blob/<branch>/agents/reviews/<TASK-ID>/evidence/01-….png?raw=true) |

CI: <check run results for the head commit>

### Review status (filled by the workflow)
- QA (qa-reviewer): PENDING → `agents/reviews/<TASK-ID>/report.md`
- PM (pm-lead): —
- Merge: human, after QA PASS + PM APPROVE
