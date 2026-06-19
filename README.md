Anchor is a device management server build in Go.

It offers capabilities to manage fleet of connected devices. Devices can be connected over various protocol frontend or thru gateway using the server API.

the server expose a web UI (HTMX) for human and a REST API for backend integration.

## MQTT broker authentication

Anchor can act as the source of truth for Mosquitto device authentication and topic authorization checks through the `mosquitto-go-auth` HTTP backend.

Configure device MQTT credentials from the Devices page. Each configured device has:

- an MQTT username and password;
- a fixed data publish topic: `dev/{orgID}/{deviceID}/data`;
- a fixed task receive topic: `dev/{orgID}/{deviceID}/task`;
- optional gateway mode, which allows publishing data for other devices in the same organisation.

Anchor exposes these broker-facing endpoints:

- `POST /mqtt/auth`: username/password check;
- `POST /mqtt/superuser`: always denies superuser access;
- `POST /mqtt/acl`: topic authorization check.

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

The HTTP backend also supports form params; Anchor accepts both JSON and form requests.

## MQTT client

Anchor can also connect to a MQTT 5 broker as an internal client. It consumes telemetry publishes to materialize the device twin and publishes device tasks on task topics.

Configure the internal client with:

- `ANCHOR_MQTT_BROKER_URL`: broker URL, such as `mqtt://127.0.0.1:1883`;
- `ANCHOR_MQTT_CLIENT_ID`: MQTT client ID, defaults to `anchor-ingest`;
- `ANCHOR_MQTT_USERNAME`: broker username, defaults to the client ID;
- `ANCHOR_MQTT_PASSWORD`: broker password, defaults to a random per-process value;
- `ANCHOR_MQTT_QOS`: subscription and task publish QoS, defaults to `0`.
- `ANCHOR_FOTA_DOWNLOAD_BASE_URL`: optional public base URL used in FOTA task download URLs, such as `https://anchor.example.com`. When unset, FOTA tasks use a relative `/org/{orgID}/releases/{releaseID}/binary` path.

Ingestion uses MQTT 5 content type metadata. If the content type contains `json` or `cbor`, Anchor decodes that format. If no content type is provided, Anchor tries CBOR first, then JSON. Decoded object payloads are flattened into twin property paths.

When the broker uses Anchor's `/mqtt/auth` and `/mqtt/acl` endpoints, the configured internal client may subscribe to `dev/+/+/data`, receive matching telemetry topics, and publish task messages to `dev/{orgID}/{deviceID}/task`. It is not allowed to publish device data or become a superuser.

## Logging

Use Go's `log/slog` package for application logs.

- `Info`: one-time lifecycle messages that do not recur during normal operation, such as `app started`.
- `Debug`: recurring operational messages. Debug logs should stay quiet by default.
- `Warn`: recoverable problems that may need attention but do not require immediate intervention, such as malformed JSON.
- `Error`: serious problems that require immediate human intervention, such as database corruption, crashes, or application bugs.

## License

Anchor is licensed under the GNU Affero General Public License v3.0. See [LICENSE](LICENSE).

Contributions require a signed [Contributor Copyright Assignment Agreement](CLA.md). See [CONTRIBUTING.md](CONTRIBUTING.md).
