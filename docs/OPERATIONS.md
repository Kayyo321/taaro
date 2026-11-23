# Taaro deployment and recovery

## Repository access

For a dedicated deployment, set `TAARO_REPOSITORY` to the exact GitHub `owner/repo` used by the App and give the instance a fresh SQLite database or restore a backup from that repository's dedicated instance. Install the App with access to the selected repository only. Keep one server process per database. Store its database, keys, and environment in a directory accessible only to its service account.

```dotenv
TAARO_REPOSITORY=acme/widgets
TAARO_LISTEN_ADDR=127.0.0.1:8080
TAARO_PUBLIC_URL=https://taaro.example.com
TAARO_DB_PATH=/var/lib/taaro/taaro.db
```

Set `TAARO_API_TOKEN` to a strong random bearer token. Scope applies to authenticated API and handoff requests and to signed webhooks. Out-of-scope requests receive HTTP 404, and out-of-scope repository webhooks are acknowledged and ignored. Installation events apply registration and removal only to the configured repository. Startup checks the database for stored records outside the configured scope. Leave `TAARO_REPOSITORY` empty for a trusted group that shares access across its repositories. All token holders share the instance's permissions.

For hosted access, terminate HTTPS at a trusted reverse proxy and keep the backend listener on loopback or a protected private network. Use HTTPS in the CLI and runtime adapter URLs. Forward GitHub webhook bodies and signature headers unchanged. If the proxy supplies the API token for browser handoff links, authenticate and authorize the reader for the repository before injecting it. Keep the token out of GitHub Check Run URLs, query strings, and logs. Configure webhook delivery and local development with [the workflow guide](WORKFLOW.md).

## Backup

Run the server binary with `-backup` to snapshot the SQLite database while the server is active, including committed changes in the write-ahead log. The destination must be a new file on a filesystem that supports hard links and directory synchronization. The tool verifies database integrity and syncs the snapshot and its destination directory before reporting success. [SQLite snapshot behavior](https://sqlite.org/lang_vacuum.html#vacuum_with_an_into_clause)

The [example systemd unit](../deploy/taaro.service) reads `/etc/taaro/server.env` and `/etc/taaro/github.pem` as credentials and sets `TAARO_DB_PATH=/var/lib/taaro/taaro.db`. Provide the active database path explicitly when running backup or webhook status outside the unit, so an environment-file default cannot select a different database.

```bash
TAARO_DB_PATH=/var/lib/taaro/taaro.db \
  /usr/local/bin/taaro-server -env /etc/taaro/server.env \
  -backup /var/backups/taaro/widgets-2026-11-22.db
```

Keep backups separate from the live volume and restrict access to the same group as the database. Store deployment configuration and secrets through their existing secret-management workflow. Retain backups only as long as their data remains authorized, including after repository removal. A backup contains the recorded webhook and coordination state at snapshot time.

## Restore

1. Choose a trusted backup whose repository permissions are still current. Stop the instance you are replacing so only one process handles live webhooks.
2. Restore into a fresh path. Keep the old database and any SQLite sidecars available for rollback. The tool rejects an existing destination or existing sidecar files.

   ```bash
   TAARO_DB_PATH=/var/lib/taaro/widgets-restored.db \
     /usr/local/bin/taaro-server -env /etc/taaro/server.env \
     -restore /var/backups/taaro/widgets-2026-11-22.db
   ```

3. Ensure the service account can read and write the restored file, then point `TAARO_DB_PATH` at it in the systemd unit or an override. Start the service with the matching `TAARO_REPOSITORY` and current credentials. Verify `taaro health` and query a known PR and fixture via `taaro context` and `taaro artifact get`. Check the delivery state and reconcile or redeliver relevant events after the backup time. Pending work captured in the snapshot can retry on startup.

Exercise restores using an isolated instance with no production GitHub credentials or webhook traffic. Verify real records from the backup before relying on it for recovery.
