# Verified implementation checkpoint

Snapshot: 2026-11-22. The published integration now includes the per-draft payment and reversal UI (F2-UI-07 R1).

## Published and accepted

- Repository: `ch0c01dxyz/UMNIA-APP`.
- Integration branch: `f1-03-identity` at [`dba278e0b21e8e8cccd08f1942f07a57e34058db`](https://github.com/ch0c01dxyz/UMNIA-APP/commit/dba278e0b21e8e8cccd08f1942f07a57e34058db) (M, merge of PR #6).
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

PR #5 documented the state at PR #4; PR #6 fulfilled its recommendation to implement UI07. Review [Ringkasan pembayaran: workspace payment summary](../reviews/f2-post-payments-next-slice.md) against the separate Finance onboarding option. F2-UI-08 is not assigned, frozen or implemented; the UI07 results are not evidence for that candidate.
