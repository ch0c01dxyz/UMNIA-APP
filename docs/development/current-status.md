# Verified implementation checkpoint

Snapshot: 2026-11-28. The integration branch includes per-draft payments and reversals (F2-UI-07 R1), the workspace payment summary (F2-UI-08), and Finance Basic activation from the browser (F2-UI-09).

## Current integration

- Repository: `ch0c01dxyz/UMNIA-APP`.
- Integration branch `f1-03-identity` is at [`e4b71cd64200cd5bbd33de7105dca2ae5e350aa3`](https://github.com/ch0c01dxyz/UMNIA-APP/commit/e4b71cd64200cd5bbd33de7105dca2ae5e350aa3), the merge of [PR #10](https://github.com/ch0c01dxyz/UMNIA-APP/pull/10).
- In **Fitur**, an owner can confirm activation of **Keuangan Sederhana**. The API checks module-management permission and entitlement. The page reads back the installation state and uses a status read to recover after a missing response. Keyboard focus follows the result or returns to the retry button if reading fails again. Operator entitlement provisioning stays separate.
- Before merge, the [UI09 push](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/46123462360/attempts/2) and [PR run](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/46123465171/attempts/2) each passed 252 Go tests including subtests, the Nuxt build, and 226/226 Playwright tests (197 owner-flow, 29 proxy/session). The merge tree matches the tested head.
- [Integration push 46131279358](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/46131279358) records the Go, Nuxt, and Playwright gates for the merge commit. The Go integration gate passed 252 tests including subtests, with zero failures or skips, and Nuxt built successfully.
- A Windows Compose walkthrough using the merge tree passed in Chromium on a fresh workspace. It covered registration and invitation, activation of Rental and Finance Basic with synthetic operator grants, customer and rental setup, agreed price, handover, return, and payment approved by a separate manager. The IDR row showed one posted payment and the expected stored amounts: 100000 paid, 200000 outstanding, and 300000 agreed.

## UI08 acceptance

- [PR #8](https://github.com/ch0c01dxyz/UMNIA-APP/pull/8) merged at [`4d88460f9cabf7dd0e1eac70d3f1559ad54e801e`](https://github.com/ch0c01dxyz/UMNIA-APP/commit/4d88460f9cabf7dd0e1eac70d3f1559ad54e801e) after the documentation checkpoint in [PR #7](https://github.com/ch0c01dxyz/UMNIA-APP/pull/7).
- UI08 adds **Ringkasan pembayaran** from **Draf sewa**. It shows workspace payment totals and counts per currency for authorized readers using the existing summary GET.
- Before merge, the [UI08 push](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/45105242622) and [PR run](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/45105248023) each passed 252 Go tests including subtests, the Nuxt build, and 225/225 Playwright tests (196 owner-flow, 29 proxy/session). The merged tree differs from that tested feature tree only by the two documentation files from PR #7. All 20 UI08 files match the tested head byte for byte.
- GitHub Actions blocked [UI08 integration push #97](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/46113288506) at startup because of billing. The UI09 integration suite includes the UI08 regression cases.

## UI07 acceptance

- UI07 integrated on `f1-03-identity` at [`dba278e0b21e8e8cccd08f1942f07a57e34058db`](https://github.com/ch0c01dxyz/UMNIA-APP/commit/dba278e0b21e8e8cccd08f1942f07a57e34058db) (M, merge of PR #6).
- [PR #5](https://github.com/ch0c01dxyz/UMNIA-APP/pull/5) merged two documents at `3e98dda0201bb06b34b620cf21f20d97315db9f6`: the previous checkpoint and the [UI07 payment proposal](../reviews/f2-ui-07-payment-ui-proposal.md). Its [earlier integration push](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/44978742159) passed 167/167 Playwright tests before PR #6 merged UI07 into `f1-03-identity`.
- [PR #6](https://github.com/ch0c01dxyz/UMNIA-APP/pull/6) merged the final R1 head [`5991d6c982ba30ae12285c957bb7d81eb4da3027`](https://github.com/ch0c01dxyz/UMNIA-APP/commit/5991d6c982ba30ae12285c957bb7d81eb4da3027) at M. With Finance active and entitled, an owner or manager can propose a payment or full reversal from a rental draft; approval requires a different authorized actor. R1 checks the posted record against the proposal: a conflict or contradictory history is not proof of approval.
- **Exact-M verification:** [push #89, run 44986090887, attempt 6](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/44986090887/attempts/6) succeeded. All three jobs checked out M: `business-api` passed 252 Go tests including subtests (199 top-level); `web` built Nuxt; `owner-flow-e2e` passed 202/202 Playwright tests (175 owner-flow, 27 proxy/session). This is the integration push, not a synthetic PR merge run.

`main` is still the initial blueprint at `a107924cc7050f101865bce54c7b715669d56a49`, not the implementation branch.

## Earlier checkpoints

- Checkpoint merged via [PR #3](https://github.com/ch0c01dxyz/UMNIA-APP/pull/3): `docs/f2-checkpoint-next-slice` (`f452643e33c1b46059529724ad94e06e66e93d23`) into `f1-03-identity`, merge commit `12c24e0edf85a113f713e62276731b639b16254f`. 2 files (+97/-10).
- Rental custody UI merged via [PR #4](https://github.com/ch0c01dxyz/UMNIA-APP/pull/4): `feat/f2-ui-06-custody` (`6a5de4e41dc50a06a7254c0b118531f97ae08f60`) into `f1-03-identity`, merge commit `bd1d35b0de82314b4e5970d41aaf231ea01e7256`. Exactly 15 paths (6 source, 8 images, 1 manifest; +3792/-8).
- [PR #4 integration push 44867779294](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/44867779294) verified `bd1d35b`: 199 top-level Go tests (252 with subtests), web checks and 167/167 Playwright tests (142 owner, 25 proxy), including UI06 custody and recovery. The [previous checkpoint](https://github.com/ch0c01dxyz/UMNIA-APP/blob/3e98dda0201bb06b34b620cf21f20d97315db9f6/docs/development/current-status.md?plain=1#L9-L14) retains the intermediate CI receipts.
- Earlier integration (2026-11-19): PR #1 (cleanup) and PR #2 (F2-UI-05 draft prices) merged into `f1-03-identity` at `13bfa099cbb2a2cfbd71869d9db287c01e6c8c29`, verified by [push 44540362581](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/44540362581).

## Evidence limits

The [UI07 screenshot manifest](../../design/screenshots/f2-ui-07/manifest.json) distinguishes live states from injected responses. Controlled browser responses and races test UI behavior, not database isolation; backend evidence comes from the Go integration suite. This checkpoint records no deployment, release, pilot or final F2 certification and does not establish production readiness.

## Next gate

Repeat the release walkthrough on a native Linux installation with disposable data. The Windows walkthrough used a temporary Business API and PostgreSQL stack with a production Nuxt build. Full deployment topology, including Admin and RustFS, is outside that walkthrough.

Keep the [F2 gate](../../FAST_TRACK.md#empat-fase-langsung-implementasi) open until the walkthrough and remaining MVP scope are verified. Confirm Inventory Basic scope beyond individually tracked rental units and define the applicable tax cases before claiming the gate is complete.
