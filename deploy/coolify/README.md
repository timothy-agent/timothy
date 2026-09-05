# Deploying Timothy on Coolify

[Coolify](https://coolify.io) deploys Timothy as a **Docker Compose**
resource from a Git source. The stack is nine containers, two networks
and four volumes, so the single-Dockerfile resource type cannot host it.

`docker-compose.yml` in this directory is `deploy/release/docker-compose.yml`
adapted for a repo checkout behind Coolify's proxy; it pulls the same
published `ghcr.io/timothy-agent/timothy-*` images and builds nothing.

## Before you start

`sandboxd` mounts `/var/run/docker.sock`. On a Coolify server that socket
controls every other application Coolify manages on that host, not just
Timothy — this grants root-equivalent access to all of them. Run Timothy
on a host dedicated to it, or remove the `sandboxd` service and its entry
in `brain`'s `depends_on` and run without missions.

## 1. Create the resource

**New Resource → Docker Compose**, Git source pointing at this repository
(or your fork).

| Field | Value |
|-------|-------|
| Base Directory | `/deploy/coolify` |
| Compose file | `docker-compose.yml` |
| Branch | `main`, or a release tag |

## 2. Environment variables

Compose interpolation fails the deployment outright without the first
three:

| Variable | Value |
|----------|-------|
| `TIMOTHY_VERSION` | Newest release tag without the leading `v`, e.g. `0.1.0-alpha.69` |
| `POSTGRES_PASSWORD` | `openssl rand -hex 24` |
| `TIMOTHY_MASTER_KEY` | `openssl rand -base64 32` |
| `TIMOTHY_API_TOKEN` | `openssl rand -hex 32` |
| `TIMOTHY_PUBLIC_URL` | The public HTTPS URL, e.g. `https://timothy.example.com` |
| `DOCKER_SOCK_GID` | `stat -c '%g' /var/run/docker.sock` on the Coolify host |

`TIMOTHY_PUBLIC_URL` builds the connector OAuth redirect; that URL plus
`/v1/connectors/oauth/callback` is what goes in the Google OAuth client's
authorized redirect URIs. A wrong value fails connectors with no obvious
error.

`DOCKER_SOCK_GID` defaults to `0`, which is Docker Desktop's socket
group; on Ubuntu it is typically `988`. A wrong value is fatal rather than
degraded — `sandboxd` fails closed, logs `permission denied while trying to
connect to the docker API at unix:///var/run/docker.sock`, exits 1, and takes
the whole deployment down with it. The symptom is a 502 from the proxy, since
Coolify stops the other containers too.

**Back up `TIMOTHY_MASTER_KEY` somewhere outside Coolify.** On a Compose
install it lives in `.env` next to the data; here it lives in Coolify's
own database, which is not part of a volume snapshot. A backup is the
`pgdata` volume *plus* this key — without it every stored secret is
unrecoverable.

## 3. Domain

Assign the domain to the **`web`** service, port 8080. Nothing else is
reachable from outside: `web`'s nginx proxies `/v1` to `brain` on the
internal network, and no service publishes a host port.

## 4. Mission sandbox image

`MISSION_SANDBOX_IMAGE` is an environment variable `sandboxd` hands to
the Docker socket, not a compose service, so `docker compose pull` never
fetches it. Pull it on the host once per version:

```sh
docker pull ghcr.io/timothy-agent/timothy-sandbox:<TIMOTHY_VERSION>
```

Skip this and missions fail at first sandbox start. Repeat on every
upgrade.

## 5. Verify

```sh
# searxng got its inline config (1 = the json format search_web needs)
docker exec <searxng-container> grep -c json /etc/searxng/settings.yml

# proxy is routing to web
curl -sI https://<domain>/ | head -1
```

Then open `https://<domain>/#token=<TIMOTHY_API_TOKEN>` — there is no
login page; that magic link signs the web UI in and stores the token in
`localStorage`. Go to **Settings → Providers** and add one: a fresh
install has no providers and no routes, and answers nothing until the
first provider bootstraps them.

## Upgrading

Bump `TIMOTHY_VERSION`, pull the matching sandbox image (step 4), and
redeploy. Migrations run automatically on start. Downgrading is not
supported once a newer version's migrations have run.

## Notes

- **Ollama on the host**: `host.docker.internal` does not resolve on
  Linux Docker Engine. Add
  `extra_hosts: ["host.docker.internal:host-gateway"]` to `gateway`.
- **Speech-to-text**: set `COMPOSE_PROFILES=whisper` and
  `WHISPER_URL=http://whisper:8001`. The model holds ~3 GiB RSS at idle.
- Keep volume preservation enabled for `pgdata`, `workspace` and
  `attachments`.
