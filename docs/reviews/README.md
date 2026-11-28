# Review index

Start with the [verified checkpoint](../development/current-status.md) and [GitHub workflow](../development/github-workflow.md). Reports alongside this index retain their original task-specific conclusions and limitations; their historical status lines are not the current project status.

Keep concise contracts, decisions and acceptance summaries here. Keep new raw logs and temporary probes under ignored `.local/` or in linked GitHub Actions artifacts.

## Current proposals

- [F2 Inventory Basic](f2-inventory-basic-contract.md) proposes counted stock movements, balances, and corrections for the MVP.

## Historical evidence packages

The following packages were removed from the current tree during the 2026-11-19 cleanup. Their original files remain available in Git history at the pinned implementation checkpoint and in the local hash-verified archive.

| Package | Pinned history |
| --- | --- |
| Foundation remediation | [Original review and evidence](https://github.com/ch0c01dxyz/UMNIA-APP/tree/be34a24e6f73e04c77263cecb1d43dd59e72c616/docs/reviews/f1-foundation-remediation-review) |
| Upgrade final guards | [Original evidence](https://github.com/ch0c01dxyz/UMNIA-APP/tree/be34a24e6f73e04c77263cecb1d43dd59e72c616/docs/reviews/f1-upgrade-final-guards) |
| Upgrade safety | [Original evidence](https://github.com/ch0c01dxyz/UMNIA-APP/tree/be34a24e6f73e04c77263cecb1d43dd59e72c616/docs/reviews/f1-upgrade-safety-review) |
| Windows review fixes | [Original scripts and evidence](https://github.com/ch0c01dxyz/UMNIA-APP/tree/be34a24e6f73e04c77263cecb1d43dd59e72c616/docs/reviews/f1-windows-review-fixes) |

The API workflow helper still used by the Windows upgrade scenarios now lives at [deploy/windows/tests/api-workflow.sh](../../deploy/windows/tests/api-workflow.sh). Its content is unchanged; the active caller uses the new path.

The local transition archive also preserves unpublished session contracts, accepted UI05 R1 packets, prompts and review evidence. They are indexed locally rather than committed as hundreds of historical working files.
