# Verified implementation checkpoint

Snapshot: 2026-11-28. The integration branch includes the per-draft payment and reversal UI (F2-UI-07 R1) and the read-only workspace payment summary (F2-UI-08). GitHub Actions did not start the jobs on the UI08 integration commit.

## Current integration

- Repository: `ch0c01dxyz/UMNIA-APP`.
- Integration branch: `f1-03-identity` at [`4d88460f9cabf7dd0e1eac70d3f1559ad54e801e`](https://github.com/ch0c01dxyz/UMNIA-APP/commit/4d88460f9cabf7dd0e1eac70d3f1559ad54e801e), merge of [PR #8](https://github.com/ch0c01dxyz/UMNIA-APP/pull/8) after the documentation checkpoint in [PR #7](https://github.com/ch0c01dxyz/UMNIA-APP/pull/7) merged at `9d5897ef29365f0b2f12c75e22edaa36da926242`.
- UI08 adds **Ringkasan pembayaran** from **Draf sewa**. It shows workspace payment totals and counts per currency for authorized readers using the existing summary GET. Finance activation and entitlement provisioning remain separate flows.
- Before merge, the [UI08 push](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/45105242622) and [PR run](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/45105248023) each passed 252 Go tests including subtests, the Nuxt build, and 225/225 Playwright tests (196 owner-flow, 29 proxy/session). The merged tree differs from that tested feature tree only by the two documentation files from PR #7. All 20 UI08 files match the tested head byte for byte.
- **Exact integration CI blocked.** GitHub Actions marked [push #96](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/46113170654) and [push #97](https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/46113288506) failed before starting any job. The annotations cite a billing or spending limit restriction. Once Actions can schedule jobs again, rerun #97 on `4d88460f9cabf7dd0e1eac70d3f1559ad54e801e` and record its result.

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

## Next decision

The [reporting proposal](../reviews/f2-post-payments-next-slice.md) is the historical UI08 contract. The next candidate is Finance Basic onboarding for an already-entitled workspace owner. The **Fitur** page currently activates only Rental Basic, although the backend and proxy already expose module state and activation. A focused proposal should cover explicit owner confirmation, the backend permission and entitlement checks, and a disposable browser test that starts from an inactive Finance module. Operator entitlement provisioning is a separate task.
