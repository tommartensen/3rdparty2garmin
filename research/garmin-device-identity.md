# Garmin Device Identity and First-Party Analytics

## Problem

Activities uploaded to Garmin Connect from wahoo2garmin show up in the
activity list, but Garmin's first-party analytics ignore them:

- **Load / Load Focus** (acute & chronic training load)
- **Readiness**
- **Training Status / Training Effect**
- **VO2max updates**
- **Intensity minutes** (in practice these are also affected)

The same uploads from mywhoosh2garmin *do* count toward all of these.

## Root Cause

Garmin Connect computes first-party analytics only for activities whose
FIT `file_id` message claims a **Garmin manufacturer**. Files from other
manufacturers are imported as plain activities, but excluded from the
physiological/fitness pipeline.

Verified against a real workout stored in `wahoo2garmin.db`: Wahoo's
cloud-served FIT files carry

```
file_id:   manufacturer=32 (Wahoo Fitness)  product=43 (ELEMNT)
device_info: manufacturer=32 for the head unit,
             0xFFFF (invalid) for sensors without identity
```

mywhoosh2garmin's README documents the same finding: *"Unknown device —
Garmin ignores training effect and VO2max from unknown manufacturers."*

## Fix: Device Spoofing

Before upload, decode the FIT file, rewrite the device identity, and
re-encode (see `internal/fit/fix.go`, ported from mywhoosh2garmin's
`fitfix.go`):

| Field (file_id + all device_info) | Value |
|---|---|
| Manufacturer | Garmin (1) |
| Product | Fenix 6S (3288) |
| Serial number | fixed fake value (3420897194) |

The transform also backfills session-level averages (avg power, HR,
cadence) from record data when the session message reports them as
invalid/zero — only fills, never overwrites device-computed values.

Implementation uses `github.com/muktihari/fit` v0.27.1 (decode via
`filedef` listener, re-encode with protocol version 2.0). Processing
happens at upload time (`handlers/garmin.go`), so FIT files already
stored in the DB don't need re-syncing. If a file fails to decode, the
upload is rejected with an error so the user can fix or replace the
file — a raw upload would land in Connect without any analytics.

Verified on a real Wahoo file: manufacturer/product/serial rewritten,
all 4845 records preserved, file size +0.4% from re-encoding.

## Notes

- Temperature stripping from mywhoosh2garmin's `fitfix.go` was **not**
  ported: MyWhoosh writes bogus temperature values in virtual rides,
  but Wahoo temperature readings are genuine and display fine in
  Garmin Connect.
- The choice of Fenix 6S as the spoofed product is arbitrary (it's what
  mywhoosh2garmin uses); any real Garmin product ID works. The serial
  number is a fixed constant, not derived from the user.
- Upload mechanics also turned out to matter: mywhoosh2garmin uses the
  device-sync upload (`POST /upload-service/upload`, multipart with
  filename — the path the Garmin Connect mobile app uses), while
  wahoo2garmin originally used the import-style `POST /upload/fit` with
  a raw octet-stream body. An upload with spoofed identity but via the
  import endpoint still produced no intensity minutes (observed
  2026-11-20), so wahoo2garmin was switched to the device-sync endpoint
  for parity. See garmin-connect-upload.md.
