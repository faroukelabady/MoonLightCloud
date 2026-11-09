# Create Stores and manage device access

Use the Cloud **Stores** page. A full human session with `devices.manage` is
required. Creating a Store requires all-Stores access. Device actions require
access to the Store that owns the device. Every action asks for a fresh code
from the account's authenticator app. A code can approve only one action; wait
for the next code before another action.

## Connect a shop

1. In Retail, open **Settings → Store** and copy its Store ID. Retail keeps
   authority over this identity; do not create a different ID for the same shop.
2. In Cloud **Stores**, select **Create store**. Enter that ID, the shop name
   and the same IANA timezone (for example `Africa/Cairo`). Enter a fresh OTP.
3. Open the Store group and select **Create device**. Enter a clear device
   name and a fresh OTP.
4. Copy the complete device credential from the one-time dialog. It has the
   existing `device_id.credential_id.secret` format. After closing the dialog
   there is no readback route or button. The server retains a salted keyed
   verifier, never this raw value.
5. In Retail **Settings → Cloud sync**, enter the Cloud URL and paste the
   credential. Test the connection, store it, then sync.

An existing Store ID cannot be overwritten by Create store. Use its existing
Store group to add a device. Device creation, credential storage and the Store
binding commit in one transaction; an invalid/inactive Store leaves no orphan
device. Store registration from that Retail installation remains compatible.

## Replace credentials or revoke access

**Regenerate credentials** is available for active, Store-bound devices. A fresh
OTP approves the replacement. Previous credentials stop authenticating; copy
the new one into Retail. If the response is lost, generate another replacement
with a fresh OTP. An old secret cannot be recovered.

**Revoke device** requires a fresh OTP and revokes the device and all its
credentials atomically. It retains the device row, binding and business history.
A revoked device cannot rotate its credentials or silently reactivate. Create a
new device to reconnect. An all-Stores manager may also revoke an unbound
legacy device; restricted accounts cannot manage that infrastructure.

Credential responses have `Cache-Control: no-store`. The dialog keeps secrets
only in component memory, clears them on close/unmount, and never stores them
in browser storage or URLs. Lists and audits contain metadata only. Clipboard
contents are the operator's explicit copy for pasting into Retail.

There is no schema migration. Deploy the Cloud API and dashboard together.
