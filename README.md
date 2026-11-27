# 3rdparty2garmin

Sync workouts from third-party platforms (Wahoo, MyWhoosh) to Garmin Connect.

## Prerequisites

- Go 1.22+ (uses `net/http` routing with method patterns)
- A Wahoo Cloud API application (sandbox or production)

### Register a Wahoo App

1. Go to https://developers.wahooligan.com/cloud and sign in.
2. Create a new application (sandbox is fine for development).
3. Set the redirect URI to `https://localhost:8080` (the OAuth callback
   is handled on the root path).
4. Note your **Client ID** and **Client Secret**.

### Create `.env`

```
WAHOO_CLIENT_ID=your_client_id
WAHOO_CLIENT_SECRET=your_client_secret
```

### Generate TLS Certificate

The Wahoo OAuth redirect requires HTTPS. On first start the server
generates a self-signed localhost certificate (`cert.pem`, `key.pem`) if
the files don't exist; to use your own instead, generate it before
starting:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -days 365 -nodes -keyout key.pem -out cert.pem \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```

## Install

Download a prebuilt binary for your platform (linux/darwin, amd64/arm64):

```sh
curl -fsSL https://raw.githubusercontent.com/tommartensen/3rdparty2garmin/main/download.sh | bash -s -- v1.2.0
# or from a checkout: ./download.sh v1.2.0 /usr/local/bin/3rdparty2garmin
```

The script detects OS and architecture, downloads the matching release
binary, and verifies it against the release checksums.

## Run

```sh
go run main.go
```

Open https://localhost:8080 in your browser (accept the self-signed certificate warning).

## Usage

1. Click **Connect to Wahoo** and authorize the application.
2. Your workout list appears with date, type, duration, distance, and calories.
3. Connect **Garmin** (email/password) — required for uploads. Connecting
   **MyWhoosh** is optional and adds its workouts to the list.
4. Click **Refresh List** to pull new workouts from the connected sources.
5. Click **Upload** on a workout to download its FIT file and upload it to
   Garmin in one step, or **Upload All to Garmin** for the workouts in view.

Synced workouts persist across server restarts in `3rdparty2garmin.db` (SQLite).

### Multiple accounts

Each Wahoo login gets its own user row with its own workouts and its own
Garmin/MyWhoosh connections — accounts are fully separated. **Logout** also
signs you out of Wahoo (via SAML single logout), so on a shared device the
next person to connect lands on Wahoo's login form instead of being silently
signed in as the previous user.
