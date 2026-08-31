# Application-backend Device API

The `api` device protocol is for devices relayed by a trusted application
backend—for example, a locally connected device reached through a phone application. The
backend authenticates to Anchor; the device itself has no Anchor transport
credential.

## Provisioning tags

Both `PUT`/`POST /api/v1/devices/{deviceID}` and
`POST /api/v1/devices/bulk-upsert` accept an optional `tags` array on each
device. Omitting `tags` preserves current assignments, supplying an array
replaces the complete set, and `[]` clears it. Tags are trimmed, lowercased,
validated, and returned alphabetically in provisioning results. A supplied
array containing invalid tags, duplicates after normalization, or more than 32
tags is rejected without changing that device.

## Setup and credentials

1. In **Organisations**, create an API credential and retain the token shown
   once. An organisation administrator can disable it or rotate it later.
2. Create a device model whose protocol is **API**.
3. Create a device using that model. No MQTT or CoAP credential is requested.

Rotating a credential invalidates the old token. Update the backend's secret
store before it makes another request. Any enabled credential for the
organisation can relay any API device in that organisation.

Send every check-in to the conventional path:

```http
POST /api/v1/devices/{deviceID}/check-in
Authorization: Bearer anc_org_...
Content-Type: application/json
```

The body is limited to 64 KiB. An empty object is a heartbeat and task poll:

```json
{}
```

Telemetry is flattened into twin paths while the original `data` object is
retained as a raw event:

```json
{
  "observed_at": "2026-07-17T10:04:12Z",
  "data": {
    "battery": {"percent": 82},
    "firmware": {"version": "1.4.2"}
  }
}
```

Report task progress or completion with the Anchor task ID:

```json
{
  "task_status": {
    "task": 12,
    "status": "success",
    "msg": "updated"
  }
}
```

Allowed statuses are `in_progress`, `success`, and `failure`. Telemetry and
task status are processed independently after the envelope is accepted, so a
valid component can commit even when the response reports an error in the
other component.

Every valid check-in returns a task envelope. No work is represented
explicitly:

```json
{"task": null}
```

Read and write tasks retain typed protocol-neutral parameters. A FOTA task
contains an anonymous artifact URL:

```json
{
  "task": {
    "id": 12,
    "type": "fota",
    "parameters": {
      "url": "https://anchor.example.com/org/42/releases/9/binary"
    },
    "created_at": "2026-07-17T10:00:00Z",
    "expires_at": "2026-07-18T10:00:00Z"
  }
}
```

Set `ANCHOR_FOTA_DOWNLOAD_BASE_URL` to Anchor's absolute, publicly reachable
HTTP(S) base URL before delivering FOTA tasks. The binary route is anonymous;
never put an organisation Bearer token in an artifact URL.

## Delivery guarantees

Task delivery is at least once. Anchor returns the same pending task until a
status report advances it, and concurrent polls may receive the same ID. The
backend and device must execute by task ID idempotently. Non-empty telemetry
also has at-least-once semantics: retrying creates another raw event, while
observation timestamps prevent stale values from replacing a newer twin or
firmware value.

MQTT topics remain conventional and are not returned by bulk provisioning:
`dev/{organisationID}/{deviceID}/data` and
`dev/{organisationID}/{deviceID}/task`. The bulk response likewise has no
per-device check-in URL.
