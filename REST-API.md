# REST API

Anchor's REST API is intended for trusted application backends, provisioning
tools, and fleet-management automation. It can create or update devices for all
supported transports. For the runtime protocol used by HTTP-connected devices,
see [DEVICE-API.md](DEVICE-API.md).

## Authentication

In **Organisations**, create an API credential and retain the token shown once.
Every REST API request is scoped to that credential's organisation and uses a
Bearer header:

```http
Authorization: Bearer anc_org_...
Content-Type: application/json
```

An organisation administrator can disable or rotate the credential. Rotation
immediately invalidates the previous token.

Authentication failures use this response shape:

```json
{
  "error": {
    "code": "unauthorized",
    "message": "Bearer token is invalid or disabled."
  }
}
```

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `PUT` or `POST` | `/api/v1/devices/{deviceID}` | Create or update one device |
| `POST` | `/api/v1/devices/bulk-upsert` | Create or update as many as 2,000 devices |

The HTTP device check-in endpoint shares the same authentication mechanism but
has a separate wire contract documented in [DEVICE-API.md](DEVICE-API.md).

## Create or update one device

The device ID in the URL is authoritative. The optional body `id`, when
present, must match it. Unknown JSON fields are rejected.

```http
PUT /api/v1/devices/relay-1
Authorization: Bearer anc_org_...
Content-Type: application/json
```

```json
{
  "device_model_id": 12,
  "software_versions": {
    "firmware": "1.4.2"
  },
  "tags": ["beta", "factory.floor"]
}
```

`device_model_id` is required and must belong to the authenticated
organisation. The model's configured protocol determines which credential
fields are accepted:

| Model protocol | Required fields | Optional fields | Rejected combinations |
| --- | --- | --- | --- |
| `api` | `device_model_id` | `software_versions`, `tags` | MQTT fields, `coap`, or `is_gateway: true` |
| `mqtt` | `device_model_id`, `mqtt_username`, `mqtt_password` | `software_versions`, `tags`, `is_gateway` | `coap` |
| `coap` | `device_model_id`, `coap.psk` | `coap.psk_identity`, `coap.enabled`, `software_versions`, `tags`, `is_gateway` | MQTT fields |

`coap.psk` must be unpadded Base64URL representing 16 to 64 bytes. An omitted
CoAP identity defaults to the device ID, and `enabled` defaults to `true`.

Changing a device to a model with another protocol removes credentials from its
previous transport. MQTT passwords and CoAP PSKs must therefore be supplied on
every upsert for those model types.

This is an upsert rather than a partial update: `device_model_id`,
`software_versions`, `is_gateway`, and the active transport credential are
replaced from the request. An omitted `software_versions` becomes an empty
object and an omitted `is_gateway` becomes `false`. Tags are the exception and
follow the preserve/replace rules below. A single-device body is limited to
1 MiB.

### Tags

Omitting `tags` preserves the current assignments. Supplying an array replaces
the complete set, and `[]` clears it. Tags are trimmed, lowercased, validated,
and returned alphabetically. A supplied array containing invalid tags,
duplicates after normalization, or more than 32 tags is rejected without
changing the device.

### Response

Successful single-device upserts return `200 OK`:

```json
{
  "id": "relay-1",
  "status": "created",
  "created": true,
  "tags": ["beta", "factory.floor"]
}
```

An update reports `"status": "updated"` and `"updated": true`. MQTT results
also return `mqtt_username`; CoAP results return `coap_identity`. Transport
secrets are never returned.

A row validation failure returns `400 Bad Request` with the error attached to
the result:

```json
{
  "id": "relay-1",
  "error": {
    "code": "device_model_not_found",
    "message": "device_model_id does not belong to this organisation."
  }
}
```

## Bulk upsert

The bulk endpoint accepts the same device fields inside a `devices` array. Each
device must include its `id`. The request body is limited to 4 MiB and 2,000
devices.

```http
POST /api/v1/devices/bulk-upsert
Authorization: Bearer anc_org_...
Content-Type: application/json
```

```json
{
  "devices": [
    {
      "id": "relay-1",
      "device_model_id": 12,
      "tags": ["beta"]
    },
    {
      "id": "sensor-1",
      "device_model_id": 8,
      "mqtt_username": "sensor-1",
      "mqtt_password": "replace-with-a-secret"
    }
  ]
}
```

Results preserve request order and are processed independently:

```json
{
  "results": [
    {
      "id": "relay-1",
      "status": "created",
      "created": true,
      "tags": ["beta"]
    },
    {
      "id": "sensor-1",
      "status": "created",
      "created": true,
      "mqtt_username": "sensor-1",
      "tags": []
    }
  ]
}
```

The endpoint returns `200 OK` when all rows have the same outcome class and
`207 Multi-Status` when successes and failures are mixed. Always inspect every
entry in `results`.

## MQTT provisioning note

MQTT topics are derived from the organisation and device IDs; they are not
returned by either upsert endpoint:

```text
dev/{organisationID}/{deviceID}/data
dev/{organisationID}/{deviceID}/task
```

MQTT devices should use MQTT 5. The complete MQTT topic, payload, and broker
contract is documented in [MQTT.md](MQTT.md).
