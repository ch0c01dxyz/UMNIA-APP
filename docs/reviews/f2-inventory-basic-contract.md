# F2 Inventory Basic contract proposal

**Status** — Proposed for product and security review. The source baseline is integration commit `e4b71cd64200cd5bbd33de7105dca2ae5e350aa3` on `f1-03-identity`.

## Outcome

The owner activates Inventory Basic for an entitled workspace. Authorized staff choose a catalog item, post a stock receipt or issue, and see the current quantity and movement history. An owner can correct a posted mistake by recording a linked reversal. A simple proof starts with 10 units received, issues 3, and reverses that issue to restore 10. The history contains all three movements and the audit events identify their actors.

## Pilot assumptions to approve

| Decision | Proposed pilot scope |
| --- | --- |
| Counted goods and units | Enroll individual catalog SKUs for counted stock. Each SKU uses one existing catalog unit and positive whole quantities. Snapshot that unit when the SKU is enrolled. |
| Stock location | One stock pool per workspace. The balance key is the workspace and catalog item. |
| Corrections | An owner posts one full reversal per original receipt or issue with a reason and source reference. A reversal must leave a non-negative balance. A replacement movement then records the corrected quantity. |

Confirm the SKU and unit list, whether the pilot needs multiple stock locations or fractional units, and whether stock corrections require a second approver. A correction that would produce a negative balance needs its own approved reconciliation rule.

## Fit with the existing implementation

- [Catalog migration](../../services/business-api/migrations/005_core_master_data.sql) already supplies workspace-scoped item identity, SKU, unit, and status. [Asset and hold migration](../../services/business-api/migrations/007_asset_availability.sql) supplies individually identified rental units and holds. Enrollment in counted stock and creation of serialized units must use the same transaction lock before testing item eligibility. Rental keeps its catalog references and serialized workflow when Inventory Basic is inactive.
- [Module guards](../../services/business-api/modules.go) already provide workspace locking, versioned activation, and entitlement checks. The [runtime registry](../../services/business-api/migrations/003_modules.sql) references `inventory.stock.read` as an optional Spa dependency. Propose `inventory.basic` as its provider once the stock read path is available. Manifest metadata and the readiness check must reflect the real provider. The current [operator CLI](../../services/business-api/cmd/grant-entitlement/main.go) handles Rental and Finance. A scoped entitlement grant for the Inventory module and version is a release prerequisite. Apply the existing operator role and audit model, with a separate security review of grant permissions.
- Reuse the [core stock contract](../02-core-data.md#stock-semantics) for source references and event identities, and the [module lifecycle contract](../03-module-engine.md#lifecycle-state-machine) for deactivation and retention.

## Posting and reading

1. Use tenant-scoped foreign keys on enrollment, movement, and reversal links. A movement records workspace, item, recorded unit, kind, quantity, event ID, source reference, actor, and server time. A reversal also references the original movement and records its reason. Keep the ledger append-only and write its audit event in the same database transaction.
2. Reuse tenant transactions and the workspace lock for posting, eligibility checks, and activation or entitlement changes. Verify the authorized stock scope inside that transaction before processing a replay or making a new write. Use the existing UUID `Idempotency-Key` convention scoped to the workspace and normalized request. An identical replay returns the original movement and emits no additional audit event. A changed payload with the same key returns a conflict.
3. Derive the balance from the signed sum of committed movements for the workspace and item. Use PostgreSQL exact arithmetic and canonical integer strings at the browser boundary. Reject out-of-range quantities, overflow, and any movement that would make the balance negative. Use a grouped balance read and an indexed, deterministically paginated history read.
4. Require `view_workspace` for reads. Live balances and stock writes require effective module access. Receipts and issues also require `manage_business_data`. Restrict reversals to the workspace owner. Keep authorized history readable after deactivation or revoked entitlement. Retain the ledger, unit snapshot, and audit when access changes.

## Implementation gates

- **Backend and operations.** Add scoped enrollment and ledger persistence, the balance and movement API, guarded activation, and the operator entitlement path. Cover fresh and retained migrations. Validate the live capability against the real stock query and test the operator path with synthetic credentials.
- **Browser.** Use the existing module card and workspace navigation. A minimum browser flow activates stock, posts a receipt and issue, checks the resulting balance, and reverses the issue through an explicit confirmation. Show rejection and retry states using server evidence.
- **Concurrency and recovery.** Two simultaneous issues of 7 from a balance of 10 produce one success, one conflict, and balance 3. Replays preserve movement and audit counts. A repeated reversal cannot post a second reversing movement. Inject a posting failure and verify rollback of movement and audit. Include the maximum representable quantity and verify exact values through the browser.
- **Boundaries.** Verify cross-workspace denial, cashier write denial, owner-only reversal, and deactivation or revocation racing with a write. Check retained history, independent Rental behavior, and prevention of counted enrollment on a SKU with serialized units. Test reverse ordering of the two enrollment and asset-creation operations under concurrency.

The first implementation should follow the approved pilot decisions and keep stock receipts, issues, balances, and linked corrections in one reviewable slice. Transfers, unit conversion, valuation, and procurement belong to later inventory contracts.
