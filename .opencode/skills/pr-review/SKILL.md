---
name: pr-review
description: Review changed code for regressions, contract changes, and compatibility risks
---

When reviewing code:

- Focus on behavior changes first
- Prefer concrete regressions over style comments
- Call out HTTP/API contract changes
- Watch for returned error changes and nil/error semantics
- Return only grounded findings
