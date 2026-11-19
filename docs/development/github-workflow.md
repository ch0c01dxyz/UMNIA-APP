# GitHub delivery workflow

GitHub is the normal delivery path for authorized implementation and maintenance work. A task includes its bounded implementation, appropriate checks, commit and push; it does not need a separate manual patch-transfer or publication approval round. More specific user restrictions take precedence.

1. Read `FAST_TRACK.md`, [current status](current-status.md), the task contract and current Git state. Confirm the base branch and preserve existing work. Use a task branch and one writer per checkout.
2. State the file scope and acceptance conditions. Implement the smallest complete change and run checks appropriate to its risk. Keep security review and required platform evidence.
3. Review the diff, stage explicit paths, commit and push the task branch. Never include credentials, raw session logs, unrelated work or local memory databases.
4. Review the actual GitHub diff and CI for the same full commit SHA. Report the branch, SHA, comparison or PR link, check results and unresolved limits. A successful earlier SHA is not proof for a changed commit.
5. Apply in-scope corrections on the same branch and push follow-up commits. Reuse accepted evidence only where its source and environment still match. Mark pending or unavailable checks honestly.

Merge, deployment, force-push and history rewriting require explicit authorization. Do not disable checks, rewrite history or discard someone else's work to simplify delivery. Independent review remains required for security-critical changes.

## Branch base during the transition

At the 2026-11-19 checkpoint, `main` remains the initial blueprint (`a107924`), while `f1-03-identity` contains the accepted implementation (`be34a24`). Base implementation work on the verified integration head and target comparisons or PRs to that branch until an authorized integration changes this arrangement. Do not accidentally include all implementation history in a cleanup PR to `main`.

## Keep the repository navigable

| Content | Location |
| --- | --- |
| Application, services and migrations | Existing `apps/`, `services/` and deployment paths |
| Reusable deployment test helpers | `deploy/windows/tests/` |
| Task contracts | Existing `tasks/` and relevant design documents |
| Concise decisions and accepted review summaries | `docs/reviews/`, linked from its index |
| Current checkpoint and workflow | `docs/development/` |
| Temporary logs, probes and transfer packets | Ignored `.local/logs/`, `.local/probes/`, `.local/transfers/` |
| Personal handoffs and local archives | Ignored `.local/` |
| Host-owned harness state | Ignored `.harness-memory/`; leave it in place |

Prefer GitHub Actions run links and their artifacts to committed raw logs. Keep required durable visual evidence with its task; temporary probes belong in `.local/`. Manual patch bundles are a fallback when GitHub is unavailable or specifically requested, not the routine handoff.

Use one concise task/acceptance record and GitHub follow-up commits instead of creating another prompt and report for every correction. Historical evidence remains reachable through pinned Git links in the [review index](../reviews/README.md).
