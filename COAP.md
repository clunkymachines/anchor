# CoAP/DTLS frontend

Anchor's CoAP support uses a separate frontend process. Devices connect to
CoAPS (DTLS 1.2) on UDP `5684`; the frontend's control HTTP listener and the
Anchor internal HTTP API must remain on a private network. Configure the same
high-entropy bearer token in both processes. Do not expose plaintext CoAP,
database credentials, or the bearer token to devices.

The browser UI displays and imports device PSKs as hexadecimal. The bulk
provisioning API continues to use unpadded Base64URL. Generated keys contain
16 random bytes; imported keys must decode to 16–64 bytes. The
PSK identity is case-sensitive, globally unique, valid UTF-8, and defaults to
the device ID. Replacing or disabling a credential invalidates the old
association immediately; the stored PSK is never shown on ordinary reads.

The v1 application resources are Confirmable CBOR requests:

- `POST /dp` carries a non-empty CBOR map and updates the device twin.
- `POST /hb` has an empty body and updates last-seen connectivity without an
  event.
- `PUT /tasks/{taskID}/status` reports `in_progress`, `success`, or `failure`.

Assembled request and response bodies are limited to 64 KiB. READ and WRITE
tasks use literal absolute resource paths, sequential Confirmable exchanges,
and CBOR. Multi-resource writes are not atomic. FOTA sends a small CBOR
control request containing an absolute HTTP(S) artifact URL; firmware is not
uploaded through CoAP. Device telemetry and task status are at-least-once.

The frontend foundation is configured with:

| Variable | Default | Purpose |
| --- | --- | --- |
| `COAP_UDP_LISTEN_ADDR` | `:5684` | DTLS/CoAP UDP address |
| `COAP_CONTROL_LISTEN_ADDR` | `:8081` | private control HTTP address |
| `ANCHOR_INTERNAL_URL` | `http://localhost:8080` | Anchor private HTTP base URL |
| `COAP_INTERNAL_BEARER_TOKEN` | required | shared internal bearer |
| `COAP_HTTP_TIMEOUT` | `10s` | frontend-to-Anchor request timeout |
| `COAP_EXCHANGE_TIMEOUT` | `15s` | one device CoAP exchange timeout |
| `COAP_CID_LENGTH` | `8` | fixed CID length; only `8` or the CID-off fallback `0` is accepted |
| `COAP_IDLE_SWEEP_INTERVAL` | `1m` | inactive-association sweep interval |
| `COAP_MAX_ASSOCIATIONS` | `1000` | active association cap |
| `COAP_MAX_CONCURRENT_HANDSHAKES` | `128` | handshake cap |
| `COAP_MAX_BODY_BYTES` | `65536` | assembled body cap |

CID-capable devices retain their DTLS association across NAT tuple changes;
devices without CID must perform a new handshake. The frontend has no durable
queue or database: Anchor remains the only task queue and recovery source. A
frontend dispatch is accepted only when the device has an active association
and no operation is already executing for it; otherwise Anchor keeps the task
pending. On authentication, heartbeat, or telemetry, the frontend asks Anchor
for the single current pending task instead of maintaining a local backlog.

The DTLS endpoint permits only `TLS_PSK_WITH_AES_128_CCM_8` and
`TLS_PSK_WITH_AES_128_CCM`, requires the extended master secret, and enables
Pion replay protection. CID is reported as negotiated only when the device also
offers RFC 9146 CID support.

CID migration requires the same frontend process and in-memory DTLS context to
remain alive across device cycles. A frontend restart intentionally discards
that context and requires a fresh handshake. Anchor disables go-coap's default
16-second DTLS inactivity close and delegates association expiry to the
registry sweep. The private metrics endpoint
reports `cid_negotiated`, `cid_length`, `cid_packet_received`,
`cid_packet_routed`, `peer_address_changed`, and `coap_request_received` for
diagnosing negotiation and tuple migration. Set `COAP_CID_LENGTH=0` only as a
temporary fallback when clients cannot migrate a retained CID session.
`cid_packet_received` starts at the established DTLS connection, after Pion's
UDP CID demultiplexer; `cid_packet_routed` additionally confirms the request
matched the authenticated association. Raw datagrams rejected by the
demultiplexer remain visible only in a packet capture.

Run the two processes separately:

```sh
go run .
go run ./cmd/coap-frontend
```

Expose only the frontend UDP address to devices. Keep the frontend control HTTP
address and Anchor's `/internal/coap/v1` routes on a trusted private network.
