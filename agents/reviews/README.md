# QA evidence packs

One folder per task, written by `qa-reviewer` on the PR branch:

```
agents/reviews/<TASK-ID>/
  report.md            QA report (rounds appended) + PM verdict
  evidence/
    00-ci-checks.png   CI check runs for the reviewed commit (+ .txt)
    01-<ac>.png        rendered from real command output by agents/tools/render_evidence.py (+ .txt full output)
    ...
```

The Jira ticket links to these files; pm-lead embeds the images in the PR description when raising the PR, so the
human reviewer sees the evidence inside the PR.
