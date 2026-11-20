# 3rdparty2garmin

Sync workouts from third-party platforms (Wahoo, MyWhoosh) to Garmin Connect.

## Prerequisites

- Go 1.22+ (uses `net/http` routing with method patterns)
- A Wahoo Cloud API application (sandbox or production)

### Register a Wahoo App

1. Go to https://developers.wahooligan.com/cloud and sign in.
2. Create a new application (sandbox is fine for development).
3. Set the redirect URI to `https://localhost:8080/callback`.
4. Note your **Client ID** and **Client Secret**.

### Create `.env`

```
WAHOO_CLIENT_ID=your_client_id
WAHOO_CLIENT_SECRET=your_client_secret
```

### Generate TLS Certificate

The Wahoo OAuth redirect requires HTTPS. Generate a self-signed certificate for local development:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
  -days 365 -nodes -keyout key.pem -out cert.pem \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
```

## Run

```sh
go run main.go
```

Open https://localhost:8080 in your browser (accept the self-signed certificate warning).

## Usage

1. Click **Connect to Wahoo** and authorize the application.
2. Your workout list appears with date, type, duration, distance, and calories.
3. Click **Sync** to download a workout's FIT file into the local database.
4. Click **Download** to save the FIT file to your computer.

Synced workouts persist across server restarts in `3rdparty2garmin.db` (SQLite).
