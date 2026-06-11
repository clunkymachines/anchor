# Anchor Application Design

## Device List Rule

The device listing must stay protocol-neutral.

Do not add MQTT-, CoAP-, LwM2M-, BLE-, or transport-specific columns to the main device list. Use generic columns such as `Communication`, `Status`, or `Access`, and represent configured protocols as compact protocol labels or pills.

Protocol-specific fields such as MQTT username, derived topics, gateway mode, certificates, keys, endpoints, or bearer tokens belong in the relevant communication tab on the create, edit, or detail screen, not in the fleet list.
