# Garmin Connect Upload Research

## Reference Implementation

The Go project [jdelrue/mywoosh2garmin](https://github.com/jdelrue/mywoosh2garmin) implements both Garmin SSO auth and FIT file upload in Go. Key files:
- `garmin/sso.go` — 9-step SSO authentication flow
- `garmin/client.go` — token management, FIT upload
- `garmin/tokens.go` — OAuth1/OAuth2 token structs and persistence

Dependencies: `github.com/dghubble/oauth1` (OAuth1 signing), `net/http` (stdlib).

---

## Authentication: Two Approaches

### Approach A: Programmatic SSO (mywoosh2garmin style)

A 9-step OAuth1→OAuth2 flow that logs in without a browser:

1. **Fetch OAuth consumer credentials** from `https://thegarth.s3.amazonaws.com/oauth_consumer.json`
2. **Create HTTP client with cookie jar** for session tracking
3. **User-Agent**: `com.garmin.android.apps.connectmobile` (emulates Android app)
4. **GET** `https://sso.garmin.com/sso/embed?id=gauth-widget&embedWidget=true&gauthHost=https://sso.garmin.com/sso` — sets session cookies
5. **GET** `https://sso.garmin.com/sso/signin?id=gauth-widget&embedWidget=true&...` — extract CSRF token from HTML: `name="_csrf" value="(.+?)"`
6. **POST** credentials + CSRF token to same signin URL:
   ```
   POST https://sso.garmin.com/sso/signin?...
   Content-Type: application/x-www-form-urlencoded
   Body: username=<email>&password=<password>&embed=true&_csrf=<token>
   ```
   Parse response HTML `<title>`: "Success" → proceed, "MFA" → not supported, other → failed
7. **Extract ticket** from success HTML: `embed\?ticket=([^"]+)"`
8. **Exchange ticket for OAuth1 token**:
   ```
   GET https://connectapi.garmin.com/oauth-service/oauth/preauthorized?ticket=<ticket>&login-url=https://sso.garmin.com/sso/embed&accepts-mfa-tokens=true
   ```
   Signed with OAuth1 consumer credentials (empty token). Response: URL-encoded `oauth_token=...&oauth_token_secret=...`
9. **Exchange OAuth1 for OAuth2 bearer token**:
   ```
   POST https://connectapi.garmin.com/oauth-service/oauth/exchange/user/2.0
   ```
   Signed with OAuth1 consumer+token credentials. Response JSON:
   ```json
   {
     "access_token": "...",
     "refresh_token": "...",
     "expires_in": 3600,
     "refresh_token_expires_in": 31536000
   }
   ```

**Token refresh**: When OAuth2 expires, repeat step 9 with cached OAuth1 token. When OAuth1 expires (~1 year), full re-login needed.

**Risk**: Garmin blocked direct programmatic SSO at the Cloudflare level in March 2026. The mywoosh2garmin flow may still work by emulating the Android app user-agent, but could break.

### Approach B: Browser-Based Service Ticket Bootstrap

1. User logs into Garmin Connect in their browser
2. User extracts a `serviceTicketId` (from DevTools or a redirect URL)
3. App exchanges the ticket for DI OAuth2 tokens:
   ```
   POST https://diauth.garmin.com/di-oauth2-service/oauth/token
   grant_type=...service_ticket
   client_id=GARMIN_CONNECT_MOBILE_ANDROID_DI_2025Q2
   service_ticket=<ST-xxxxx>
   service=https://connectapi.garmin.com
   ```
4. Response: `access_token`, `refresh_token`, `jwt_fgp`
5. Token refresh:
   ```
   POST https://connect.garmin.com/services/auth/token/refresh
   Cookie: JWT_FGP=<jwt_fgp>
   Body: {"refresh_token": "<token>"}
   ```

**Pro**: Resilient to Cloudflare blocks. **Con**: Requires manual browser step on first login.

---

## FIT File Upload

> **Note:** Uploaded activities only count toward Load, Readiness, Training
> Status, and other first-party analytics if the FIT file's device identity
> claims a Garmin manufacturer — see [garmin-device-identity.md](garmin-device-identity.md).

### Endpoint (device-sync, forwards to Strava) — **this is the one we use**

```
POST https://connectapi.garmin.com/upload-service/upload
Content-Type: multipart/form-data
Authorization: Bearer <access_token>
User-Agent: GCM-iOS-5.19.1.2
DI-Backend: connectapi.garmin.com
NK: NT

Multipart field: "file" = <FIT file bytes>, filename="workout.fit"
```

> **Note:** mywhoosh2garmin uploads via this endpoint and its activities
> count toward intensity minutes / Load / Readiness; uploads via the
> import-style `/upload/fit` endpoint below did not. 3rdparty2garmin was
> switched to this endpoint for parity (Nov 2026).

### Endpoint (import-style, does NOT forward to Strava)

```
POST https://connectapi.garmin.com/upload-service/upload/fit
Content-Type: application/octet-stream
Authorization: Bearer <access_token>
DI-Backend: connectapi.garmin.com

Body: raw FIT file bytes
```

### Response

```json
{
  "detailedImportResult": {
    "failures": [],
    "successes": [{"internalId": "<activity_id>", ...}]
  }
}
```

### Error codes
- **409**: Duplicate activity (already uploaded)
- **401**: Token expired (refresh and retry)

---

## Required Headers for All API Requests

| Header | Value | Purpose |
|--------|-------|---------|
| `Authorization` | `Bearer <access_token>` | OAuth2 auth |
| `User-Agent` | `GCM-iOS-5.19.1.2` (upload) or `com.garmin.android.apps.connectmobile` (SSO) | App emulation |
| `DI-Backend` | `connectapi.garmin.com` | Backend routing |
| `NK` | `NT` | Internal Garmin flag |

For Approach B, also: `Cookie: JWT_FGP=<jwt_fgp>`

---

## Rate Limits

- General API: ~100 requests/minute
- Upload: no documented limit
- HTTP 429 on rate limit (no Retry-After header)
- File size limit: 25 MB

## Token Lifetimes

| Token | Expiry |
|-------|--------|
| OAuth1 token (Approach A) | ~1 year |
| OAuth2 access token | ~1 hour (A) or ~24 hours (B) |
| OAuth2 refresh token | ~1 year (A) or ~30 days (B) |
| Service ticket | seconds |
