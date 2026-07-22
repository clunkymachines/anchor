# Fleet Simulator

The Anchor fleet simulator provisions a set of MQTT devices, connects one client per device, and continuously publishes CBOR telemetry. It is intended for development, demonstrations, and load testing.

## Prerequisites

Before starting the simulator:

1. Run Anchor and Mosquitto with the [Anchor HTTP authentication callbacks](MQTT.md#mosquitto-setup).
2. Activate the MQTT integration in Anchor.
3. Create a device model and note its numeric ID.
4. Open **Organisations**, create an API credential, and retain the token shown after creation.

## Run a Local Fleet

```sh
go run ./cmd/fleet-sim \
  -anchor-url http://localhost:8080 \
  -api-token "$ANCHOR_SIM_API_TOKEN" \
  -mqtt-url mqtt://localhost:1883 \
	-organisation-id 1 \
  -model-id 1 \
  -fleet-size 10 \
  -secret local-simulator-secret
```

The simulator bulk-provisions the devices through `POST /api/v1/devices/bulk-upsert`, derives conventional MQTT topics from the configured organisation and device IDs, derives deterministic MQTT passwords from the supplied secret, connects the clients, and runs until interrupted. Set `-organisation-id` (or `ANCHOR_SIM_ORGANISATION_ID`) explicitly.

Use `go run ./cmd/fleet-sim -help` for the complete flag reference. Every flag also has an `ANCHOR_SIM_*` environment-variable equivalent.

## Environment Variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `ANCHOR_SIM_ANCHOR_URL` | `http://localhost:8080` | Anchor base URL |
| `ANCHOR_SIM_API_TOKEN` | none | Organisation API bearer token |
| `ANCHOR_SIM_MQTT_URL` | `mqtt://localhost:1883` | MQTT broker URL |
| `ANCHOR_SIM_MODEL_ID` | `0` | Existing device model ID |
| `ANCHOR_SIM_ORGANISATION_ID` | `0` | Organisation that owns the simulated devices |
| `ANCHOR_SIM_FLEET_SIZE` | `1000` | Number of simulated devices; maximum `2000` |
| `ANCHOR_SIM_DEVICE_PREFIX` | `sim-` | Device ID prefix |
| `ANCHOR_SIM_START_INDEX` | `1` | First numeric device index |
| `ANCHOR_SIM_USERNAME_PREFIX` | `sim-` | MQTT username prefix |
| `ANCHOR_SIM_SECRET` | none | Secret used to derive MQTT passwords |
| `ANCHOR_SIM_FIRMWARE` | `sim-1.0.0` | Reported firmware version |
| `ANCHOR_SIM_TELEMETRY_INTERVAL` | `1m` | Telemetry publish interval |
| `ANCHOR_SIM_QOS` | `0` | MQTT QoS: `0`, `1`, or `2` |
| `ANCHOR_SIM_CONNECT_CONCURRENCY` | `25` | Maximum concurrent MQTT connections |
| `ANCHOR_SIM_LOG_INTERVAL` | `30s` | Aggregate metrics log interval |
| `ANCHOR_SIM_PROVISION_TIMEOUT` | `10m` | Bulk provisioning timeout |

Start with a small fleet and confirm device connectivity and telemetry in Anchor before increasing `-fleet-size`. The default is 1,000 devices and the maximum is 2,000, but broker, database, and host limits still apply.
