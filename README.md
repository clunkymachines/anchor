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

Anchor admins configure and activate the internal client from **Integrations > MQTT with Mosquitto**. The saved settings include the broker URL, client ID, username, password, and QoS. Changes take effect without restarting Anchor.

`ANCHOR_FOTA_DOWNLOAD_BASE_URL` remains an optional application setting used in FOTA task download URLs, such as `https://anchor.example.com`. When unset, FOTA tasks use a relative `/org/{orgID}/releases/{releaseID}/binary` path.

Ingestion uses MQTT 5 content type metadata. If the content type contains `json` or `cbor`, Anchor decodes that format. If no content type is provided, Anchor tries CBOR first, then JSON. Decoded object payloads are flattened into twin property paths.

When a decoded top-level `firmware` telemetry value is a string, Anchor trims surrounding whitespace and stores it as `devices.software_versions["firmware"]`. Non-string `firmware` values remain telemetry only. This reported firmware version is used to match devices to model-scoped firmware releases for CVE status.

When the broker uses Anchor's `/mqtt/auth` and `/mqtt/acl` endpoints, the configured internal client may subscribe to `dev/+/+/data`, receive matching telemetry topics, and publish task messages to `dev/{orgID}/{deviceID}/task`. It is not allowed to publish device data or become a superuser.

## Fleet simulator

Organisation admins can create API credentials from the Organisations page. The token is shown once and can be used for provisioning through:

```sh
POST /api/v1/devices/bulk-upsert
Authorization: Bearer anc_org_...
Content-Type: application/json
```

The fleet simulator provisions devices with that API token, then connects one MQTT client per simulated device using deterministic per-device MQTT passwords. It publishes CBOR telemetry only and runs until interrupted.

Example local run:

```sh
go run ./cmd/fleet-sim \
  -anchor-url http://localhost:8080 \
  -api-token "$ANCHOR_SIM_API_TOKEN" \
  -mqtt-url mqtt://localhost:1883 \
  -model-id 1 \
  -fleet-size 10 \
  -secret local-simulator-secret
```

Scale `-fleet-size` up to `1000` once Anchor and the broker are running with the HTTP auth callbacks above. If the fresh schema changed, delete and recreate the local database before the run.

## CVE scanning

Firmware releases can include optional `.spdx` SBOM files. Anchor stores those files with the release artifacts and scans them asynchronously for CVEs when an SBOM is present or manually rescanned from the release detail page.

Anchor invokes the external Grype CLI instead of embedding scanner code. Install `grype` on the server `PATH`, or set `ANCHOR_GRYPE_PATH` to the scanner binary path. If Grype is missing or exits with an error, Anchor records the scan run as failed and leaves manual rescan available.

Scan results are scoped to the current SBOM set for a release. Replacing the SBOM removes the old SPDX files, clears prior scan runs and findings for that release, and enqueues a new scan.

## Logging

Use Go's `log/slog` package for application logs.

- `Info`: one-time lifecycle messages that do not recur during normal operation, such as `app started`.
- `Debug`: recurring operational messages. Debug logs should stay quiet by default.
- `Warn`: recoverable problems that may need attention but do not require immediate intervention, such as malformed JSON.
- `Error`: serious problems that require immediate human intervention, such as database corruption, crashes, or application bugs.

## License

Anchor is licensed under the GNU Affero General Public License v3.0. See [LICENSE](LICENSE).

Contributions require a signed [Contributor Copyright Assignment Agreement](CLA.md). See [CONTRIBUTING.md](CONTRIBUTING.md).
