# MyWhoosh API Research

Findings from porting the MyWhoosh import from the standalone
mywhoosh2garmin app into this repo (`internal/mywhoosh/client.go`).
MyWhoosh has no public API documentation; everything below was
reverse-engineered (originally by the mywhoosh2garmin project).

## Authentication

MyWhoosh has **no OAuth** — plain email/password login:

```
POST https://services.mywhoosh.com/http-service/api/login
Content-Type: application/json

{
  "Username": "<email>",
  "Password": "<password>",
  "Platform": "Android",
  "Action": 1001,
  "CorrelationId": "<random uuid>",
  "DeviceId": "<random uuid>",
  "Authorization": ""
}
```

Response:

```json
{
  "Success": true,
  "Message": "",
  "AccessToken": "...",
  "RefreshToken": "...",
  "WhooshId": "..."
}
```

Notes:
- The `Platform: "Android"` and `Action: 1001` fields appear required.
- `CorrelationId` / `DeviceId` are random UUIDs per login — no device
  registration is validated server-side.
- Both tokens are JWTs with an `exp` claim (no `iat`). Lifetimes:
  **15 days** for the access token, **60 days** for the refresh token.
- The reference client ignored the `RefreshToken` and made the user
  re-log in on 401. 3rdparty2garmin refreshes instead (see below).

## Token Refresh

Not in the mywhoosh-community/mywhoosh-api README; documented only in
its open PR #2 (captured from the desktop app's traffic). Verified
against the live API on 2026-11-20:

```
GET https://services.mywhoosh.com/http-service/api/tokens/generate
Authorization: Bearer <refresh_token>
```

Response: `{"AccessToken", "RefreshToken", "Username", "WhooshId"}`.

Observed behavior:
- No request body, `Action`, or `DeviceId` needed.
- Both tokens are rotated, and **both expiries restart from the
  refresh time** (sliding window): refreshing at least every 60 days
  should keep a session alive without the password.
- Old tokens are **not revoked**: the old refresh token can still be
  used to refresh again, and the old access token still works.
- Passing the access token instead of the refresh token returns
  **HTTP 426** with `{"error_message": ...}`, not 401. The response
  for an *expired* refresh token is untested — treat any non-200 as
  "re-login required".

## Activity Listing

```
POST https://service14.mywhoosh.com/v2/rider/profile/activities
Content-Type: application/json
Authorization: Bearer <access_token>

{"sortDate":"DESC","page":1}
```

Response (paginated):

```json
{
  "data": {
    "results": [ { ...activity... } ],
    "totalPages": 3
  }
}
```

Activity fields used:

| Field | Meaning |
|---|---|
| `id` | Activity ID (string) — primary key |
| `title` | Activity name |
| `date` | Start time as Unix epoch (float seconds) |
| `startDatetime` | Start time as `2006-01-02T15:04:05.000Z` (preferred, more precise) |
| `activityFileId` | Key for the FIT download endpoint |
| `distance` | Kilometers (note: **not** meters) |
| `rideDuration` | `"01:31:54.000"` (HH:MM:SS.mmm) |
| `watt` / `heartrate` / `elevation` / `wattPerKg` | Averages |
| `routeName` / `sportType` | Display metadata |

Results are newest-first; pages are walked until `page >= totalPages`.

## FIT File Download

Two-step:

```
POST https://service14.mywhoosh.com/v2/rider/profile/download-activity-file
Authorization: Bearer <access_token>

{"fileId":"<activityFileId>"}
```

→ returns `{"data": "<pre-signed S3 URL>"}`, then a plain GET on that
URL returns the FIT bytes. The S3 URL needs no auth.

## FIT Processing

MyWhoosh FIT files need the same Garmin device-identity spoof as Wahoo
files (see [garmin-device-identity.md](garmin-device-identity.md)), plus
one extra step: **temperature is stripped from all records** — MyWhoosh
virtual rides carry bogus temperature values. Wahoo files keep their
(genuine) temperature. This is why `fit.Fix` takes an `Options` struct
with `StripTemperature`, set per source by the upload handler.

## Architecture in 3rdparty2garmin

- `internal/mywhoosh` is a **stateless** client — tokens are passed in
  per call rather than held in client state or cached on disk (the
  reference app cached them in `~/.mywhoosh2garmin/`). Session tokens
  live in the `mywhoosh_tokens` SQLite table, one row per user, like
  `garmin_tokens`. The access token's expiry is read from its `exp`
  claim rather than stored.
- Token refresh (`withMyWhooshToken` in `internal/handlers/mywhoosh.go`):
  every MyWhoosh call refreshes first if the access token expires
  within a day, and on a 401 refreshes once and retries. If the refresh
  token is rejected (any 4xx), the stored tokens are deleted so the UI
  shows "Connect" again; 5xx/network errors keep them. Rows stored
  before refresh support have an empty `refresh_token` and are deleted
  on their first 401, requiring one re-login.
- MyWhoosh activities are stored in the same generalized `workouts`
  table as Wahoo workouts, keyed by `(user_id, source, external_id)`.
  `file_url` holds the source-specific download reference: a URL for
  Wahoo, the `activityFileId` for MyWhoosh.
- The Garmin upload pipeline (device spoof + multipart device-sync
  upload) is shared between both sources.
- MyWhoosh activity listing has no incremental mode (unlike Wahoo's
  page-by-page fetch with early stop): all pages are fetched, then
  already-known activities are skipped on upsert.
