# Wahoo Cloud API Research

## Base URL

- **Production**: `https://api.wahooligan.com`

## OAuth2 Authentication Flow

### Authorization

```
GET https://api.wahooligan.com/oauth/authorize
  ?client_id=<client_id>
  &redirect_uri=<redirect_uri>
  &response_type=code
  &scope=<scopes>
```

User is redirected to Wahoo login, then redirected back with `?code=<authorization_code>`.

### Token Exchange

```
POST https://api.wahooligan.com/oauth/token

{
  "grant_type": "authorization_code",
  "client_id": "<client_id>",
  "client_secret": "<client_secret>",
  "code": "<authorization_code>",
  "redirect_uri": "<redirect_uri>"
}
```

Response:

```json
{
  "access_token": "...",
  "token_type": "Bearer",
  "expires_in": 7200,
  "refresh_token": "...",
  "scope": "...",
  "created_at": 1234567890
}
```

### Token Refresh

```
POST https://api.wahooligan.com/oauth/token

{
  "grant_type": "refresh_token",
  "client_id": "<client_id>",
  "client_secret": "<client_secret>",
  "refresh_token": "<refresh_token>"
}
```

- Access tokens expire after **2 hours**.
- Each refresh issues a new `refresh_token`; the old one is revoked on first API call with the new token.
- Max 10 unrevoked access tokens per user.

## Required Scopes

For this project we need:
- `user_read` — required for any API access (403 without it)
- `workouts_read` — access workout history and FIT files

## Endpoints

### List Workouts

```
GET /v1/workouts?page=1&per_page=30

Authorization: Bearer <access_token>
```

Response:

```json
{
  "workouts": [
    {
      "id": 12345,
      "ascent_accum": "...",
      "cadence_avg": "...",
      "calories_accum": "...",
      "distance_accum": "...",
      "duration_active_accum": "...",
      "duration_paused_accum": "...",
      "duration_total_accum": "...",
      "heart_rate_avg": "...",
      "power_avg": "...",
      "speed_avg": "...",
      "work_accum": "...",
      "file": {
        "url": "https://cdn-url/path/to/file.fit"
      },
      "created_at": "2026-11-11T...",
      "updated_at": "2026-11-11T..."
    }
  ],
  "total": 150,
  "page": 1,
  "per_page": 30,
  "order": "desc",
  "sort": "created_at"
}
```

### Get Workout Summary (contains FIT file URL)

```
GET /v1/workouts/{id}/workout_summary

Authorization: Bearer <access_token>
```

Response includes a `file.url` field pointing to the FIT file on Wahoo's CDN.

### Download FIT File

The `file.url` from workout or workout_summary is a direct CDN link. Download it with a plain GET request — no auth header needed. CDN downloads do **not** count against API rate limits.

## Pagination

- `page` (integer, default: 1)
- `per_page` (integer, default: 30)
- Response includes: `total`, `page`, `per_page`, `order`, `sort`

## Rate Limits

### Sandbox (our current app)

| Window | Limit   |
|--------|---------|
| 5 min  | 25      |
| 1 hour | 100     |
| 1 day  | 250     |

### Production

| Window | Limit   |
|--------|---------|
| 5 min  | 200     |
| 1 hour | 1,000   |
| 1 day  | 5,000   |

Response headers:
- `X-RateLimit-Limit` — contains `limit_day`, `limit_hour`, `limit_5min`
- `X-RateLimit-Remaining` — contains `remaining_day`, `remaining_hour`, `remaining_5min`
- `X-RateLimit-Reset` — seconds until reset

Exempted from rate limits: auth requests, token refreshes, CDN file downloads.

## Sandbox vs Production

- Sandbox and Production are **completely separate app registrations** — a sandbox app cannot be promoted to production.
- The OAuth flow, base URL, and available endpoints are identical; only rate limits differ.
- Cannot access workout data that originated from third-party apps (Wahoo-native data only) — applies to both environments.
- To go to production, you must submit a separate application for Wahoo's review.

## Gotchas

1. `user_read` scope is effectively mandatory — API returns 403 without it.
2. Token revocation is automatic on first use of a refreshed token.
3. CDN file downloads are free from rate limits.
4. Sandbox and Production are separate app registrations — cannot convert one to the other.
5. Only workout data recorded on Wahoo devices is accessible — third-party-originated workouts are excluded.

## Official Resources

- Documentation: https://cloud-api.wahooligan.com/
- Developer Portal: https://developers.wahooligan.com/cloud
