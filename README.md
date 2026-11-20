# Obico Bridge for Prusa Connect Local

Obico Bridge runs as a native extension in Prusa Connect Local 0.1.0-beta.1. It sends active print status and JPEG snapshots from each printer's assigned camera to a separately installed Obico server for AI failure detection. Obico can send its usual notifications. Control the printer in Connect Local and set Obico's failure action to **Notify**.

## Install

Download `obico-bridge-0.1.1.tar.gz` from GitHub Releases. In the deployment that runs Connect Local, install the archive with the same binary and configuration that the server uses:

```sh
prusa-connect module add -config /path/to/config.yaml \
  -from /path/to/obico-bridge-0.1.1.tar.gz -approve \
  github.com/nilsn3dp/prusa-obico-bridge@0.1.1
```

On Unraid, place the archive in the existing Connect Local configuration mount and use the existing container:

```sh
docker exec prusa-connect-local /app/prusa-connect-local module add \
  -config /config/config.yaml -from /config/obico-bridge-0.1.1.tar.gz -approve \
  github.com/nilsn3dp/prusa-obico-bridge@0.1.1
docker restart prusa-connect-local
```

Plan the restart for when the printers are idle. Connect Local loads the extension settings on startup.

After activation, open **Extensions → Obico Bridge** for the paired printers, camera, print progress and recent jobs.

## Configure

1. In Connect Local, assign a camera to each printer and check the camera tab. The camera must provide JPEG snapshots through the Connect Local API.
2. Create a Connect Local API token for an account that can observe the printers and their cameras. `GET /api/v1/devices` returns device IDs. `GET /api/v1/cameras?device=<ID>` returns the assigned camera names and their snapshot capability.
3. In your Obico server, use the manual OctoPrint linking flow to obtain a six-digit verification code per printer. Extract the release archive and run the included binary for your platform with the `link` command. Enter the code on standard input. Save the returned printer token in the extension settings.

   ```sh
   read -rs code
   printf '%s\n' "$code" | ./obico-bridge_linux_amd64 link http://127.0.0.1:3334
   unset code
   ```

4. In Connect Local, open **Settings → Extensions → Obico Bridge → Settings**. Fill in `prusa_url` with the server origin reachable from the extension process, `obico_url` with the origin of your Obico server and `prusa_token` with the API token. The default Connect Local URL is `http://127.0.0.1:18888`. Adjust it for deployments on other ports.
5. Paste the printer mappings as JSON into the **printers** secret field. Each printer requires its own Obico token. Set `camera` if more than one snapshot camera is assigned to the same printer.

   ```json
   [
     {
       "device_id": "connect-local-device-id",
       "obico_token": "obico-printer-token",
       "camera": "bed-camera"
     }
   ]
   ```

6. Save and apply Connect Local's requested restart. Enable failure detection and select **Notify** for each Obico printer.

7. For Obico alerts, history, monitoring controls and notification preferences, authorize an Obico API access token for the account that owns the paired printers. Save it as `obico_access_token` in the extension's settings. Grant read access for the dashboard and write access if operators should change monitoring or notification preferences. Use Obico's OAuth setup and revoke the credential there when it is no longer needed. [Obico OAuth guide](https://www.obico.io/docs/api/api-authentication-oauth/)
8. Set `obico_browser_url` to the Obico origin reachable by the browser if it differs from `obico_url`. Timelapse playback uses signed media links at that origin. The browser must be able to load media there.

During a test print, Obico should show the job name and progress. The first snapshot follows after the next poll when the camera has a fresh image. Look for `forwarding snapshots for job` in the Connect Local logs and for predictions in the Obico print history. Paused or disconnected printers suspend image uploads.

## Build and test

Use Go 1.26.11 and the SDK from the matching Prusa beta:

```sh
git clone --depth 1 --branch v0.1.0-beta.1 https://github.com/prusa3d/prusa-connect.git .core
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o obico-bridge_linux_amd64 .
```

The extension source uses the MIT license. The binary also uses the Prusa Connect SDK under Prusa's license and Gorilla WebSocket under its BSD license. [Prusa Connect license](https://github.com/prusa3d/prusa-connect/blob/v0.1.0-beta.1/LICENSE) · [Gorilla WebSocket license](https://github.com/gorilla/websocket/blob/v1.5.3/LICENSE)

See [Publishing](PUBLISHING.md) for Marketplace setup.
