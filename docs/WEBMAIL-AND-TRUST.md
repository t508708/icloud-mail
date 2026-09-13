# iCloud receiver and browser trust

The original receiver uses `imap.mail.me.com:993` with certificate-verified TLS
1.2 or newer. It never selects a different port automatically. An Apple
`AUTHENTICATIONFAILED` response occurs after the TLS connection succeeds;
check the account-specific App password rather than changing SSL settings.

## Optional Web Mail receiver

Protocol reference:
[iCloud-Privacy-Mail](https://github.com/q1953258942/iCloud-Privacy-Mail),
`internal/app/icloud_client.go`, revision `3a839c7a6fb1a2f33b6cc680f4be63ae224fd165`.
The reference implements real Mail APIs in addition to the separate Hide My
Email address directory. `pyicloud` and the HME-only API are not interchangeable
Mail implementations.

Select `iCloud 网页收件` in the account's receiver controls after authenticating
the iCloud Web connection. This uses the saved encrypted Web session, not the
Apple Account creation session or the IMAP App password. Existing accounts keep
IMAP until the administrator explicitly selects Web Mail. Forwarded/custom
IMAP accounts keep their configured mail source.

Each authenticated retrieval request shares the existing per-alias in-flight
operation and ten-second result cache. Account serialization, the configured
minimum fetch interval, timeout, and global concurrency limit still apply.
There is no added poller, keepalive, automatic login, or protocol fallback.

Requests use the account's dynamic `mccgateway` service. Older encrypted sessions
can derive it from the matching Apple `mailws`/`maildomainws` host. The four
read operations are folder query, thread search, thread metadata, and message
text parts. Directory sync still manages addresses and does not trigger these
mail requests.

Limits and semantics:

- Only INBOX is read, examining up to 20 recent threads and 128 metadata items.
  This is a bounded recent-mail scan, not an all-history import or an upstream
  recipient search. A busy inbox can have older target mail outside this window.
- Full parsed recipient equality is required before downloading text. Before
  publication, the existing trusted HME/delivery-header classifier rechecks the
  target. Display names and substring matches never grant access to another
  alias's content.
- Only text parts are fetched, with at most eight parts per message and a
  512 KiB total body bound. Attachments are not downloaded. The local MIME file
  is a text projection identified by `X-Mail-Archive-Source`; it is not an
  original RFC822 download. Existing complete MIME archives remain intact.
- INBOX UIDVALIDITY and UID identify the same Apple mailbox objects as IMAP.
  An observed same-UID Message-ID and UIDVALIDITY match was checked against a
  working account before integration. Existing archive keys deduplicate these
  objects. Web reads never advance account or alias IMAP cursors.
- `dontMarkAsRead` is always true. Legacy consumption remains local in Web Mail
  mode and does not queue an IMAP flag update. Existing queued IMAP work is
  suspended while that account selects Web Mail.
- Web authentication errors stop further mail requests until a newer trusted
  Web login/validation is saved. They retain independently useful creation and
  directory sessions. A successful later mail publication clears mail errors.

## Trust retention

Both login protocols already send `rememberMe` and call `/2sv/trust` after
verification. Web also sends `extended_login` during its token exchange.

Apple Account now captures its own `X-Apple-TwoSV-Trust-Token`, encrypts it with
its independent session, and includes it in `trustTokens` on a subsequent
same-account, same-region login. A failed trust request is reported explicitly.
Existing connections stay active; older Account sessions acquire this newly
persisted token during the next normal login, without a forced reauthentication.

Automatic Web expiry retains only its browser trust token, account/region and
stable client ID, marking service authentication inactive. Expired service
tokens, cookies and service identifiers are discarded. The next explicit
same-account, same-region login may reuse browser trust. Explicit logout removes
it. HME directory authorization failures no longer erase an otherwise validated
Web login. These changes preserve Apple-issued trust; Apple still determines
whether another verification is required.

## Validation

- Read-only live Web Mail folder and target-message requests returned HTTP 200.
  The target message also passed the project's existing delivery classifier and
  MIME projection, without writing to the production database.
- A working account negotiated IMAP TLS 1.3; both protocols identified the same
  INBOX generation and the same Message-ID for a selected UID.
- Offline end-to-end test: authenticated retrieval URL -> Web Mail fixture ->
  existing delivery classifier -> MIME projection -> archive -> OTP response.
  This succeeds with deliberately invalid IMAP ciphertext and an existing IMAP
  authentication error, writes no IMAP cursor, and leaves the other alias alone.
- Trust tests cover persistence, same-account reuse, cross-account isolation,
  automatic expiry and explicit logout. No real password login or verification
  is triggered by those tests.
- Go package tests, race checks for Apple/session/sync/store packages, and
  `go vet` pass. All 230 frontend tests pass. The transport API contract covers
  authentication, CSRF, explicit opt-in, disabled accounts, Web-login readiness
  and preserving IMAP credentials when switching back.
- Public browser checks cover transport changes with intercepted writes,
  rollback on failure, missing-login handoff, collapsed historical IMAP errors,
  390/1440-pixel layouts and same-origin assets. Production transport settings
  were unchanged by verification; public TLS and application health passed.
