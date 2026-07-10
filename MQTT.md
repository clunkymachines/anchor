# MQTT Protocol

Anchor supports MQTT devices through a broker, usually Mosquitto, with Anchor providing HTTP authentication/authorization callbacks and an internal MQTT client for ingestion and task publishing.

The protocol is designed around two per-device topics:

- Data publish topic: `dev/{orgID}/{deviceID}/data`
- Task receive topic: `dev/{orgID}/{deviceID}/task`

`orgID` is the numeric Anchor organisation ID. `deviceID` is the Anchor device ID.

## Payload Format

Devices should use MQTT 5 and set the publish content type property.

Preferred content type:

```text
application/cbor
```

Anchor also accepts JSON with `application/json`, but CBOR is the default format for task payloads sent by Anchor.

If no content type is provided, Anchor tries to decode CBOR first, then JSON.

## Device Telemetry

Devices publish telemetry to:

```text
dev/{orgID}/{deviceID}/data
```

The payload should be a CBOR map. Anchor stores the raw event and flattens decoded values into device twin paths.

Example CBOR diagnostic notation:

```cbor
{
  "battery": 87,
  "online": true,
  "location": {
    "lat": 43.6,
    "lon": 1.44
  }
}
```

This materializes twin properties like:

```text
battery = 87
online = true
location.lat = 43.6
location.lon = 1.44
```

## Tasks

Anchor publishes tasks to:

```text
dev/{orgID}/{deviceID}/task
```

Task payloads are CBOR maps with MQTT content type `application/cbor`.

Example decoded read task:

```cbor
{
  "task": 7,
  "type": "read",
  "parameters": {
    "paths": [
      "battery.percent",
      "firmware.version"
    ]
  },
  "status": "pending",
  "created_at": "2026-06-06T17:30:14Z"
}
```

Example decoded write task:

```cbor
{
  "task": 8,
  "type": "write",
  "parameters": {
    "values": [
      {
        "path": "config.sample_interval",
        "value": 60
      }
    ]
  },
  "status": "pending",
  "created_at": "2026-06-06T17:31:14Z"
}
```

Example decoded FOTA task:

```cbor
{
  "task": 9,
  "type": "fota",
  "parameters": {
    "url": "https://anchor.example.com/org/42/releases/9/binary"
  },
  "status": "pending",
  "created_at": "2026-06-06T17:32:14Z"
}
```

Fields:

- `task`: Anchor task ID. Devices must include this in status updates.
- `type`: Task kind: `read`, `write`, or `fota`.
- `parameters`: Task-specific object. Read tasks carry `paths`, write tasks carry typed JSON `values`, and FOTA tasks carry a firmware download `url`.
- `status`: Initial task status, usually `pending`.
- `created_at`: RFC3339 creation time.

FOTA download URLs are public. Configure the URL base with:

```sh
ANCHOR_FOTA_DOWNLOAD_BASE_URL=https://anchor.example.com
```

If unset, Anchor sends a relative path such as:

```text
/org/42/releases/9/binary
```

Read task values are reported as normal telemetry on `dev/{orgID}/{deviceID}/data`. Task status remains a separate device-reported update.

## Task Status Updates

Devices report task progress by publishing to their data topic:

```text
dev/{orgID}/{deviceID}/data
```

Use CBOR content type `application/cbor`.

Example in-progress update:

```cbor
{
  "task": 7,
  "status": "in_progress",
  "msg": "downloading firmware"
}
```

Example success update:

```cbor
{
  "task": 7,
  "status": "success"
}
```

Example failure update:

```cbor
{
  "task": 7,
  "status": "failure",
  "msg": "checksum mismatch"
}
```

Allowed device-reported statuses:

- `in_progress`
- `success`
- `failure`

`msg` is optional. Anchor stores the latest message, capped at 512 characters, for display with the task status.

Terminal states do not regress. Once a task reaches `success`, `failure`, or `canceled`, later status messages for the same task do not reopen it.

## Pending Task Replay

When a device subscribes to its task topic, Anchor treats that as the device being ready to receive tasks. If the device has pending tasks, Anchor republishes those pending tasks to:

```text
dev/{orgID}/{deviceID}/task
```

This is implemented through the Mosquitto ACL callback. A successful subscribe authorization on the device task topic triggers pending task replay.

## Gateway Devices

Normal devices may publish only to their own data topic and subscribe/read only their own task topic.

Gateway devices can publish data for other devices in the same organisation:

```text
dev/{orgID}/{otherDeviceID}/data
```

Gateway mode is configured on the device in Anchor.

## Anchor MQTT Internal Client

Anchor can connect to the broker as an internal MQTT client. Configure it with:

```sh
ANCHOR_MQTT_BROKER_URL=mqtt://127.0.0.1:1883
ANCHOR_MQTT_CLIENT_ID=anchor-ingest
ANCHOR_MQTT_USERNAME=anchor-ingest
ANCHOR_MQTT_PASSWORD=change-me
ANCHOR_MQTT_QOS=0
```

Variables:

- `ANCHOR_MQTT_BROKER_URL`: Broker URL. If unset, MQTT ingestion and task publishing are disabled.
- `ANCHOR_MQTT_CLIENT_ID`: MQTT client ID. Defaults to `anchor-ingest`.
- `ANCHOR_MQTT_USERNAME`: MQTT username. Defaults to the client ID.
- `ANCHOR_MQTT_PASSWORD`: MQTT password. If unset, Anchor generates a random per-process password.
- `ANCHOR_MQTT_QOS`: QoS for subscription and task publishes. Must be `0`, `1`, or `2`. Defaults to `0`.

When using Anchor's Mosquitto auth callbacks, configure `ANCHOR_MQTT_PASSWORD` explicitly. The same username/password must be accepted by Anchor's `/mqtt/auth` endpoint for the internal client.

The internal client is allowed to:

- subscribe to `dev/+/+/data`
- read matching data topics
- publish task messages to device task topics from Anchor's process

The internal client is not allowed to publish device data or become a superuser.

## Mosquitto Setup

Anchor is intended to work with Mosquitto plus the `mosquitto-go-auth` HTTP backend.

Example Mosquitto listener:

```conf
listener 1883 0.0.0.0
allow_anonymous false
```

Example `mosquitto-go-auth` configuration:

```conf
auth_plugin /etc/mosquitto/conf.d/go-auth.so

auth_opt_backends http
auth_opt_http_host 127.0.0.1
auth_opt_http_port 8080
auth_opt_http_getuser_uri /mqtt/auth
auth_opt_http_superuser_uri /mqtt/superuser
auth_opt_http_aclcheck_uri /mqtt/acl
auth_opt_http_response_mode status
auth_opt_http_params_mode json
auth_opt_http_method POST
```

Anchor accepts both JSON and form encoded callback requests.

Mosquitto access values interpreted by Anchor:

- `1`: read
- `2`: write
- `3`: read and write
- `4`: subscribe

## Device Credentials

Create MQTT credentials from the Anchor Devices UI. Each MQTT-enabled device has:

- username
- password
- enabled flag
- fixed data publish topic
- fixed task receive topic
- optional gateway permission

The device should connect to Mosquitto using the configured username/password.

## Device ACL Rules

For a normal device:

- May write: `dev/{orgID}/{deviceID}/data`
- May subscribe/read: `dev/{orgID}/{deviceID}/task`
- May not write data for other devices
- May not subscribe to other devices' task topics

For a gateway device:

- May write: `dev/{orgID}/{anyExistingDeviceID}/data`
- May subscribe/read: only its own task topic

## Example Device Flow

1. Device connects to Mosquitto with its Anchor MQTT username/password.
2. Device subscribes to `dev/{orgID}/{deviceID}/task`.
3. Anchor republishes any pending tasks for that device.
4. Anchor creates a FOTA task and publishes a CBOR task document to the task topic.
5. Device downloads the firmware package from `parameters.url`.
6. Device publishes status updates to `dev/{orgID}/{deviceID}/data`.
7. Anchor updates the task status and refreshes the device detail UI in realtime.

## Example CLI Checks

Subscribe as a device:

```sh
mosquitto_sub \
  -h 127.0.0.1 \
  -u device-001 \
  -P 'device-password' \
  -t 'dev/42/device-001/task' \
  -V mqttv5
```

Publish JSON for quick manual testing:

```sh
mosquitto_pub \
  -h 127.0.0.1 \
  -u device-001 \
  -P 'device-password' \
  -t 'dev/42/device-001/data' \
  -V mqttv5 \
  -D publish content-type application/json \
  -m '{"task":7,"status":"in_progress","msg":"manual test"}'
```

For production device firmware, publish CBOR and set:

```text
content-type = application/cbor
```
