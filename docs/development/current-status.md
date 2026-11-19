# Verified implementation checkpoint

Snapshot: 2026-11-19. This record separates published acceptance from local work; it does not certify the whole F2 phase or production readiness.

## Published and accepted

- Repository: `ch0c01dxyz/UMNIA-APP`.
- Integration branch: `f1-03-identity`.
- F2-UI-04 accepted commit: `be34a24e6f73e04c77263cecb1d43dd59e72c616`.
- Exact-SHA hosted verification: [CI run 44419060660](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/44419060660), success.
- Go: 199 top-level tests, 252 including subtests, zero failures or skips. Web checks passed. Playwright: 122/122 passed (102 owner, 20 proxy), including all five screen tests.

`main` is still the initial blueprint at `a107924cc7050f101865bce54c7b715669d56a49`. Do not interpret its older file tree as the current implementation or repeat already accepted gates solely because it is named `main`.

## Accepted locally, not yet published

F2-UI-05 R1 (per-draft prices) was reviewed against the published checkpoint above. Its local packet has 15 paths: six source files, eight images and one evidence manifest. Native Playwright evidence reports 143/143 passed (120 owner, 23 proxy); Windows Docker build and five independent browser recovery/race checks also passed. Those browser checks used controlled responses and are not new database/RLS proof. Unchanged Go evidence was reused.

The UI05 feature has no published commit or exact-SHA hosted CI yet. Its accepted packet, frozen contract and local evidence are preserved in the ignored transition archive, indexed by `.local/README.md` on the current operator machine. They are not included in the repository-cleanup change.

## Next implementation action

Recover the accepted UI05 R1 packet and contract, apply it on its own task branch from the verified integration head, validate the actual diff, then commit and push through the [GitHub workflow](github-workflow.md). Verify hosted CI on that new full SHA before claiming published acceptance. Merge and deployment remain separate authorized actions.
