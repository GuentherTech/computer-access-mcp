# Deploy the relay to Railway

The desktop agent can connect directly through Cloudflare or use a hosted relay. Railway hosts only the relay. PowerShell runs on each agent PC.

## Configure the Railway service

1. Create a Railway service from this repository, using the repository root. The checked-in `railway.json` selects Railpack and starts `./out relay`. No custom build command is needed. [Railpack detects Go and builds the `out` executable](https://railpack.com/languages/golang/).
2. Attach a persistent volume at `/data`. The relay uses Railway's supplied volume path for `registry.json`. [Volumes preserve files between deployments](https://docs.railway.com/volumes).
3. Under **Settings > Networking**, generate a public domain. Railway supplies HTTPS and the `RAILWAY_PUBLIC_DOMAIN` variable. For a custom origin, set `RELAY_PUBLIC_URL=https://relay.example.com`. [Railway variable reference](https://docs.railway.com/variables/reference).
4. Optionally set `RELAY_ENROLLMENT_KEY` to a random secret. New agents must provide it once during enrollment. Without a key, enrollment is open up to the device limit.
5. Redeploy after configuring the volume, domain, and variables. Check `https://<your-domain>/healthz`; the response is `ok`.

The relay listens on `0.0.0.0:$PORT`. The checked-in configuration uses `/healthz` as the deployment health check. [Railway injects `PORT` for the listener and health check](https://docs.railway.com/deployments/healthchecks).

Keep one replica in one region. The registry and live device connections belong to that process. Keep the volume and public hostname when redeploying so agents retain their registered service URLs. Enable Railway volume backups for the registry.

The relay refuses to start on Railway without an attached volume or a public origin. This prevents a deployment from storing device registrations in temporary storage.

## Connect an agent

1. Install the Windows agent using `computer-access-setup.exe`.
2. Open **Settings**, select **Hosted relay**, and enter the Railway HTTPS origin without a path.
3. Enter the enrollment key if configured, then select **Save**.
4. Copy each service's URL and token into the MCP client using Streamable HTTP and bearer authentication.

Build the installer with `make installer`; see the [build instructions](development.md#build-the-installer).

## Test a relay locally

Run `go run . relay` to listen at `http://127.0.0.1:8788`. Local runs save the registry to `relay-data/registry.json`. The `-listen`, `-public-url`, and `-data` flags override environment defaults.
