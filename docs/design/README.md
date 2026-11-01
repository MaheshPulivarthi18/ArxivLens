# Tech designs

One file per task that needs a design (`docs/design/<TASK-ID>.md`, from `agents/templates/design.md`).

**When a design is required:** the task is P0, adds a CLI command, table or service, or touches a shared contract
(events, HTTP API, schema, embedding model). The Jira story carries the `design-required` label. Small tasks skip it.

**Flow:** the owning developer writes the design as the first commit on the task branch → pm-lead approves or requests
changes (filling "PM decision") → only then does the developer write code. qa-reviewer later checks the implementation
against both the ticket and the approved design. A design that changed during the build is updated in the same PR.
