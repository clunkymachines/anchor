# HTTP Device API

The HTTP device API is for devices reached through a trusted application
backend—for example, a locally connected device relayed by a phone application.
The backend authenticates to Anchor; the relayed device has no Anchor transport
credential of its own.

This document covers runtime communication for device models whose protocol is
**API**. Backend provisioning is documented separately in
[REST-API.md](REST-API.md).

## Setup and credentials

1. In **Organisations**, create an API credential and retain the token shown
   once.
2. Create a device model whose protocol is **API**.
3. Create a device using that model, either in the UI or through the
   [REST API](REST-API.md#create-or-update-one-device).
4. Store the organisation Bearer token in the trusted backend, not in the
   relayed device.

An organisation administrator can disable or rotate the credential. Rotation
immediately invalidates the previous token. Any enabled credential for the
organisation can relay any API device in that organisation.

## Check in

Heartbeat, telemetry, task polling, and task status all use one endpoint:

```http
POST /api/v1/devices/{deviceID}/check-in
Authorization: Bearer anc_org_...
Content-Type: application/json
```

The body is limited to 64 KiB and must be one JSON object containing only
`observed_at`, `data`, and `task_status`. An empty object is both a heartbeat
and a task poll:

```json
{}
```

Every accepted check-in updates device connectivity and returns the current
pending task, if one exists.

## Send telemetry

Use `data` for telemetry and optionally provide an RFC 3339 observation time:

```json
{
  "observed_at": "2026-07-17T10:04:12Z",
  "data": {
    "battery": {"percent": 82},
    "firmware": {"version": "1.4.2"}
  }
}
```

When `observed_at` is omitted, Anchor uses the server receipt time. A timestamp
more than one hour in the future is rejected. Nested objects are flattened into
device-twin paths such as `battery.percent`, while the original `data` object is
retained as a raw event. Empty or absent `data` records no telemetry event.

Older observations remain in the raw event history but do not replace newer
twin values or the current firmware version.

## Poll for tasks

Every valid check-in returns a task envelope. No work is represented
explicitly:

```json
{"task": null}
```

When work is pending, `task` contains its ID, type, parameters, and lifetime:

```json
{
  "task": {
    "id": 12,
    "type": "read",
    "parameters": {
      "paths": ["battery.percent"]
    },
    "created_at": "2026-07-17T10:00:00Z",
    "expires_at": "2026-07-18T10:00:00Z"
  }
}
```

Task parameter shapes are:

| Type | Parameters |
| --- | --- |
| `read` | `{"paths":["battery.percent"]}` |
| `write` | `{"values":[{"path":"interval","value":60}]}` |
| `fota` | `{"url":"https://anchor.example.com/org/42/releases/9/binary"}` |

For FOTA, set `ANCHOR_FOTA_DOWNLOAD_BASE_URL` to Anchor's absolute, publicly
reachable HTTP(S) base URL. The generated artifact route is anonymous; never
put an organisation Bearer token in an artifact URL.

## Report task status

Report progress or completion with the Anchor task ID:

```json
{
  "task_status": {
    "task": 12,
    "status": "success",
    "msg": "updated"
  }
}
```

Allowed statuses are `in_progress`, `success`, and `failure`. `msg` is optional
and limited to 512 Unicode characters. A terminal report can receive the next
queued task in the same response.

Telemetry and task status are validated and committed independently after the
request envelope is accepted. If one component is valid and the other is not,
Anchor can persist the valid component while returning an error for the invalid
one. Clients must not assume an error response means that every submitted
component was rolled back.

## Delivery guarantees

Task delivery is at least once. Anchor returns the same pending task until a
status report advances it, and concurrent polls may receive the same ID. The
backend and device must execute each task ID idempotently.

Non-empty telemetry also has at-least-once semantics: retrying creates another
raw event, while observation timestamps prevent stale values from replacing a
newer twin or firmware value.

## Errors

Errors use a stable JSON envelope:

```json
{
  "error": {
    "code": "protocol_mismatch",
    "message": "Device is not configured for API check-in."
  }
}
```

Common HTTP statuses include `400` for an invalid envelope or component, `401`
for a missing, invalid, or disabled Bearer token, `404` for an unknown device or
task, `409` when the device model does not use the API protocol, `413` when the
body exceeds 64 KiB, and `415` when the content type is not JSON.
