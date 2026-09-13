# iCloud IMAP recipient search compatibility

The iCloud server can accept LOGIN and EXAMINE, then return
`NO [UNAVAILABLE] Unexpected exception` to `UID SEARCH HEADER X-Original-To`.
Searching `Original-Recipient` also reproduced this response. The previous
on-demand query included these fields, so a valid mailbox could repeatedly
return `SYNC_UNAVAILABLE` while account authentication remained healthy.

Read-only comparisons on the same mailbox and UID window established:

- UID-only search succeeded.
- `X-ICLOUD-HME` search succeeded.
- Combined `X-ICLOUD-HME`, `To` and `Cc` search succeeded.
- Both a single unsupported delivery field and a balanced OR containing such
  fields failed. Reducing OR nesting alone did not fix the issue.

On the normalized `imap.mail.me.com` host, candidate discovery now searches the
HME routing header, To and Cc. Other IMAP providers keep the full delivery-header
query. Candidate headers are still fetched and checked by the existing strict
recipient classifier before downloading any body; SEARCH matches alone never
grant access. UID bounds, per-alias cursors, account serialization, throttling,
BODY.PEEK and read-only mailbox selection remain in use. This adds no polling,
fallback retries or whole-mailbox body downloads.

The regression fixture reproduces Apple's error for unsupported search fields
and verifies the supported query finds HME-, To- and Cc-addressed candidates.
Existing recipient-conflict, substring, independent cursor and OTP URL tests
cover publication. A read-only production fetch also completed classification,
MIME parsing and OTP extraction before deployment.

Demand failures now carry the HTTP request ID, account/alias IDs, operation
stage and a credential-redacted error in one log entry, including failures
before fetching or during publication. Public responses retain their existing
error contract.

Mail, syncer and HTTP regression suites passed, including race checks. After
deployment, the existing public OTP URL returned HTTP 200 with two OTP records
in about five seconds. An immediate repeat returned the same records in 18 ms.
No password, mailbox transport or alias credentials were changed during repair.
