# Anchor

<p align="center">
  <img src="logo.png" alt="Anchor by Clunky Machines" width="120">
</p>

Anchor is a web application for managing connected-device fleets. It brings device inventory, telemetry, remote tasks, firmware releases, campaigns, and software vulnerability tracking into one place.

Anchor is early-stage software. Database schemas may change between versions; during development, delete and recreate local databases when the fresh schema changes.

## What You Can Do

- Organise devices by organisation and device model.
- Monitor device connectivity, reported software versions, telemetry, and raw events.
- Send read, write, and firmware-update tasks to individual devices.
- Run tasks across selected devices as campaigns and track their results.
- Store firmware releases and associate an expected release with each device model.
- Upload SPDX SBOMs and review CVEs detected in firmware releases.
- Provision devices through organisation-scoped API credentials.
- Connect MQTT devices through Mosquitto, including gateway devices.

## Quick Start

### Requirements

- Go 1.24 or newer
- SQLite, included and used by default

Mosquitto is only required when connecting MQTT devices. Grype is only required when scanning release SBOMs for CVEs.

### Start Anchor

From the repository root, set a password for the initial Anchor administrator and run the server:

```sh
ANCHOR_ADMIN_PASSWORD='choose-a-password' go run .
```

Open [http://localhost:8080](http://localhost:8080) and sign in with:

- Email: `admin@anchor.local`
- Password: the value supplied in `ANCHOR_ADMIN_PASSWORD`

Anchor creates `anchor.db`, the administrator account, and a personal organisation on the first run. Bootstrap account settings only apply when creating the first user, so set them before starting with a new database.

For a disposable local start, the default password is `anchor`. Do not use that default outside local development.

## Connect Your First Device

1. Open **Device models** and create a model with its expected heartbeat interval and protocol.
2. If the device uses MQTT, configure Mosquitto to use Anchor for authentication and ACL checks. See [MQTT setup](MQTT.md#mosquitto-setup).
3. As an Anchor administrator, open **Integrations**, configure **MQTT with Mosquitto**, and activate it.
4. Confirm that **Broker connection** reports **Connected**. If it does not, Anchor displays the latest connection reason.
5. Open **Devices**, create a device, choose the model, and set its MQTT username and password.
6. Configure the device with the displayed data and task topics, then connect it to Mosquitto.
7. Open the device in Anchor to inspect telemetry, raw events, current twin values, software versions, and tasks.

The MQTT payload and topic contract is documented in [MQTT.md](MQTT.md).

## Core Workflows

### Devices and Telemetry

The **Devices** page shows fleet connectivity, model, software, CVE, and communication status. A device detail page combines:

- current materialised telemetry values;
- recent raw device events;
- reported software versions;
- communication credentials and topics;
- active and recent tasks.

Device connectivity is calculated from the heartbeat interval configured on its model.

### Tasks and Campaigns

Use a device detail page to launch a task for one device:

- **Read** requests one or more telemetry paths.
- **Write** sends typed values to device paths.
- **FOTA** asks a device to install a firmware release.

Select devices from the inventory and choose **Create campaign** to launch the same task across a group. Anchor tracks queued, pending, in-progress, successful, failed, expired, and canceled work.

### Firmware Releases and CVEs

Create a release for a device model by uploading its firmware binary and, optionally, SPDX SBOM files. Anchor can:

- deliver the release through FOTA tasks;
- compare a device's reported firmware version with the model's expected release;
- scan the SBOM with Grype;
- group active CVEs by severity;
- record CVEs marked as not relevant.

Install `grype` on the server `PATH`, or configure `ANCHOR_GRYPE_PATH`, to enable scanning. A missing or failed scanner is reported in the release scan history and can be retried from the release page.

### Organisations and Access

Every user belongs to at least one organisation. Use the organisation picker to switch the fleet currently being managed.

- **Anchor administrators** can access every organisation and configure application-wide integrations.
- **Organisation administrators** can rename their organisation, invite or remove members, and manage API credentials.
- **Organisation members** can work with resources in organisations they belong to.

Invitation links let new users set their display name and password before joining an organisation.

## Configuration

Anchor reads application and bootstrap settings from environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `ANCHOR_HTTP_ADDR` | `:8080` | HTTP listen address |
| `ANCHOR_DB_DIALECT` | `sqlite` | `sqlite`, `postgres`, or `postgresql` |
| `ANCHOR_DB_PATH` | `anchor.db` | SQLite database path |
| `ANCHOR_DB_DSN` | none | Database DSN; required for PostgreSQL |
| `ANCHOR_ADMIN_EMAIL` | `admin@anchor.local` | Bootstrap administrator email |
| `ANCHOR_ADMIN_NAME` | `Anchor Admin` | Bootstrap administrator display name |
| `ANCHOR_ADMIN_PASSWORD` | `anchor` | Bootstrap administrator password |
| `ANCHOR_FOTA_DOWNLOAD_BASE_URL` | relative URLs | Public URL prefix used in FOTA download links |
| `ANCHOR_GRYPE_PATH` | `grype` from `PATH` | Grype executable used for CVE scans |
| `ANCHOR_COAP_ENABLED` | unset/disabled | Enable the CoAP frontend integration |
| `ANCHOR_COAP_FRONTEND_URL` | unset | Private HTTP base URL of `coap-frontend` |
| `COAP_INTERNAL_BEARER_TOKEN` | unset | Shared private bearer token for Anchor/frontend |

MQTT connection settings are stored in Anchor and managed from **Integrations**, not through environment variables.

SQLite is the tested database path. A PostgreSQL schema and PostgreSQL-specific queries are implemented, but there is currently no automated PostgreSQL integration suite. Treat PostgreSQL support as experimental.

## MQTT Connection Status

The **Integrations** page reports the internal MQTT client's live broker state:

- **Disabled**: the integration is inactive.
- **Connecting**: Anchor is waiting for the broker connection.
- **Connected**: the broker connection is established.
- **Reconnecting**: an established connection was lost and Anchor is retrying.
- **Connection failed**: the latest connection attempt failed; the broker error is shown on the page.

The status refreshes automatically. Common causes of failure include an unreachable broker URL, rejected internal-client credentials, and Mosquitto listener or TLS configuration errors.

## Further Documentation

- [MQTT protocol and Mosquitto setup](MQTT.md)
- [Fleet simulator](SIMULATOR.md)
- [Contributing](CONTRIBUTING.md)
- [License](LICENSE)

## License

Anchor is licensed under the GNU Affero General Public License v3.0. Contributions require a signed [Contributor Copyright Assignment Agreement](CLA.md).
