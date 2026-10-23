# Trae 0.13.0: independent provider and reliability boundary

## Repository and dispatch ownership

This fork is `bfSan/trae-cpa-plugin`, with Go module `github.com/bfSan/trae-cpa-plugin/plugins/trae`. The README, registry, build scripts and release workflow now publish Trae alone. Unrelated provider snapshots from the original multi-plugin checkout were removed from this working tree, not from Git history and not from the separately maintained sibling repositories. Upstream copyright and MIT licensing remain.

The former `pool` and `scheduler` packages were removed. They mixed account ranking/rotation/cooldown policy with maintenance. The replacement maintenance code enumerates the host's current credentials; it has no account selection, preference, tier ladder, rotation cursor or four-level cooldown pool. `scheduler.pick` is only a compatibility response with `Handled:false`.

Account-pool management, ranked stages, weights, routing and cross-account fallback remain in staged-scheduler/CPA. This change does **not** rewrite staged's deployed rules or copy a second scheduler there: staged already implements those responsibilities. Model display order is not account priority.

Trae keeps a bounded-duration, account/model-specific observation of actual upstream quota rejection. This is evidence and an execution guard for that exact credential/model, not a multi-account policy engine. Zero balance alone does not create an account-wide disable; fresh positive balance cannot clear unrelated model quota. A five-minute observation TTL permits a later probe; it does not assert an upstream midnight reset. Explicit release affects observations, never manual disabled state.

## Maintenance and manual intent

All mutations use fixed hash-shard account locks. The host inventory and current physical credential are re-read before use/save. Refresh replaces token fields within the current document, preserves `disabled`, `disabled_reason`, labels, model caches and unknown extensions, and refuses stale-token writes or disappeared credentials. CN and Intl refresh use their own protocol clients and the same save discipline. Saving a rotated token must succeed before execution continues.

OAuth saves likewise merge existing manual metadata. A fresh login is not implicit permission to reenable an account. Unix credential reads/writes use pinned directory descriptors, no-follow opens, regular-file/owner checks and atomic replacement. The host ABI has no atomic compare-and-swap transaction spanning outside writers: checks close the known stale-write paths but cannot eliminate the final race with an unrelated host mutation. Host-delivered AuthData has the same no-save-ACK limitation.

Periodic maintenance only operates on credentials that still exist and are enabled. Automatic check-in fires at 09:00 Asia/Shanghai; startup and reenable do not catch up a missed firing. Token keepalive checks hourly, including the first enabled maintenance tick. Neither operation selects which account should serve a chat request.

## Check-in confirmation and bounded verification

The existing Trae `checked_in` / `did_checked_in` fields remain the confirmation authority. A claim code, text message, advertised daily reward or lack of Go error is not sufficient. Manual, automatic and retry paths share account identity and the stable Trae check-in device selection.

A pending marker is saved **before** a claim is submitted. If a response is lost or verification fails, later attempts only query status rather than submitting another claim. The marker and daily attempt budget live in the credential's `checkin_state`; a process restart therefore does not erase that uncertainty. This is not a history feature and is not presented as one.

The per-account/day budget is four total attempts (initial plus up to three retries), using bounded exponential wait intervals. Explicit Trae activity rejection 9074 may be retried within that budget; it is not relabelled as generic HTTP 429. Uncertain outcomes remain verification-only. Disabling auto prevents new automatic work without erasing safety evidence. Credentials with manual disable are not automatically signed or refreshed.

The panel requires `confirmed:true`, updates only the matching account and never marks every card signed because one batch item succeeded. Rewards remain separate from spendable balance. Intl has no CN daily check-in action.

## Cache ordering

Check-in and credit snapshots carry independent observation times. Updates use compare-and-swap merging so a slow read cannot overwrite a newer receipt or balance invalidation. Checking in does not fabricate a fresh credit timestamp. Removal is based on host inventory, not an independently managed account pool.

## Recoverable OAuth

The Trae protocol is preserved: CN/SOLO/Intl client identities, PKCE, authCodeInfo/loginTraceID callback handling, remote paste, device identity/private keys and bound DeviceProof renewal are not replaced by Qoder's protocol.

New tasks use separate authenticated records under `.trae-oauth-pending`, keyed by a state digest:

`pending → exchange_started → token → result → saved`

The per-state mutex covers callback, poll and self-completion. A token checkpoint precedes identity lookup and final save. Identity/save failures reuse that token. Once an exchange may have been submitted, an ambiguous outcome blocks automatic authorization-code replay; a fresh login may be necessary. Automatic cross-origin replay of an uncertain token rotation is also prohibited; explicit unsupported endpoints may retain bounded protocol fallback.

Unix pending storage uses 0700 directories, 0600 records, no-follow file operations, HMAC authentication, process locking, atomic rename/fsync and bounded records/size/TTL. Records contain secrets in plaintext protected by filesystem permissions; HMAC is not encryption and does not defend against an attacker who can read the same owner's key. Explicit persistent mode fails closed on unsupported non-Unix storage rather than silently weakening guarantees.

Pending tasks last 15 minutes; obtained results have a bounded 10-minute recovery window, with a total maximum of 25 minutes. Host and self-save ownership are persisted separately. A host result can be replayed but is not reported as saved merely because it was delivered to the host.

**Migration:** the old unsigned, single-slot pending file is not trusted or automatically migrated. Complete or restart unfinished old login attempts after upgrading. Existing credential files do not require relogin solely because of this storage change. Host callback files are validated before reading, not immediately deleted on first read; the phase checkpoint prevents replay. The release does not claim automatic cleanup of legacy callback files.

## Unified network boundary

`netroute` owns all new HTTP routing. An empty `proxy-url` uses the initialized host bridge; an explicit valid proxy owns the route exclusively. Invalid explicit settings block HTTP, not unload the whole provider. Standalone compatibility mode uses environment proxy routing only when no bridge is installed.

A bridge call that fails is never followed by a direct retry. Direct/proxy clients do not follow redirects that could replay credentials or claim bodies. Stream responses remain streaming; stream lifetime follows cancellation rather than a short total request timeout. Returned transport errors remove token-bearing URLs and proxy secrets.

The current CPA HTTP bridge does not carry every cancellation/deadline/redirect setting. Checks before submission and reads do not create a guaranteed hard network deadline inside a blocked host callback. Host-managed request logging and network policy still belong to the host.

## Model and panel contract

The panel lists model ID/name with CN/SOLO/Intl columns, supports drag-handle ordering and preserves a single exclusion source (`oauth-excluded-models`). Saving exclusions retains unseen IDs and wildcard rules; a global exclusion is not copied into per-channel settings. Sorting patches only `model_order` and verifies host persistence plus plugin runtime.

Per-account discovery may fall back only to that account's own snapshot with matching token/variant provenance. Missing or malformed identity does not acquire the global static union as entitlements. The static response is empty; per-account discovery supplies registrations. The panel's union is explicitly a candidate view, with account differences preserved.

The current Trae parser has evidence for a single context length, not user-selectable tiers. The UI therefore shows a read-only length and no manufactured 200K/400K/1M switch. Missing multiplier/reasoning facts remain unreported, not free/1x/high defaults. As of 0.13.1, explicitly reported defaults use the WorkBuddy highlight style. A known single current context length is emphasized without inventing a default or a switching capability. No check-in history, routing-pool controls or reset-to-account-default control is introduced.

## Validation and rollout

Run offline Go race tests, `go vet`, panel Node tests and `scripts/check-release.py`. Tests cover route exclusivity, bridge envelopes/streaming, mutation uncertainty, per-state OAuth recovery, manual-state preservation, stale refresh rejection, account-local models and check-in confirmation. Live probes remain opt-in and are not part of the release test command.

Deployment must back up the old library, atomically install a new inode, restart CPA and verify the registered version, loaded inode/hash, panel contract and service health. No real claim, login or paid model experiment is required to validate this implementation. Offline tests and successful loading do not prove new upstream context/reasoning capabilities.


## 0.13.1 presentation and metadata update

The permanent page now puts account cards first, upstream activities second and the model list last. There is no model-management opener or account-observation summary. Login recovery is retained in a compact expandable area. Account filters/search are local display controls, never routing policy. Model hide/restore and drag order save immediately with busy protection, host/runtime readback and failure recovery.

Pricing and promotions are read from the already-used `get_detail_param` response rather than fabricated or manually maintained. `display_contact_config` can be a JSON string or object. `consumption_rate.data.rate` and `activity_discount.data.current.consumption_rate` are observed payable rates; campaign `after_consumption_rate` is its reported campaign rate. Discount 10 means paying 10%, not a multiplier applied again to an already-discounted rate. Membership/subsidy candidates without eligibility evidence remain uncertain.

Activity windows use explicit named time zones, ISO weekdays and overnight start-day attribution. Missing time zones do not default to the browser's zone. Metadata has a 90-minute freshness bound; crossing a known window boundary invalidates an observed current price until refreshed. The visible panel refreshes metadata periodically and at known transitions; failures mark old evidence stale. Account-specific differences and unobserved peers are not flattened into a universally valid rate. Ordinary no-discount current prices are not activity cards.

Account credit progress uses the same eligible credit packages and rounding basis as the reported balance. It excludes check-in rewards. The used portion is the consumed part of the current finite pool, not an all-time spend counter; unknown/unlimited totals do not get a fabricated percentage.

The official Max-context capability is real, but it must not be conflated with the current `solo_work_lite` request lane. See [the protocol investigation](trae-max-context.md). This release does not silently advertise/enable 1M on the existing CN executor.


## 0.13.2 daily refresh and readonly capability policy

This supersedes the 0.13.1 15-minute visible-page polling policy above. Discovery is server-owned and per credential, once per 24 hours, with explicit credential/host-auth refresh and manual directory refresh as additional triggers. The page does not issue automatic upstream refreshes on startup, visibility, timer or campaign transitions. It only updates local snapshot presentation.

Model snapshots use schema version 2. A persisted attempt checkpoint prevents repeated discovery after failure/restart. Disabled/deleted credentials are skipped. Network IO occurs outside account locks; commit rechecks token, refresh token, realm, identity and manual disable. Synchronous host-save watchers read visible or last-good data without waiting on the same flight, avoiding recursive save deadlocks. A superseded flight can restart at most once.

Authenticated read-only `solo_agent` metadata supplements the existing worklite catalog by exact model ID and credential only. It does not supply model authorization or change the chat function. A separate credential-bound input-test receipt can enable the matching declared context; evidence is not shared across accounts or realms. See [catalog policy](model-catalog-refresh.md). No SQLite dependency or fake context-selection control is added.
