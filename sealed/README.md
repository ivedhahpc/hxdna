# hxdna/sealed

Keeps an app's secrets encrypted on disk, and starts the app **locked** until a person unlocks it.
Shared by HelixOps, Provision and network-app (and the appnex-go template). Its own Go module inside
the hxdna repo, so workers — which import `hxdna` — never pull it in.

```
go get github.com/ivedhahpc/hxdna/sealed@v0.1.0
```

(Tagged `sealed/v0.x.y` in the hxdna repo.) Design and reasoning:
`docs/architecture/sealed-secrets.md` in the hive docs repo.

## How it works

```
.env          → settings that aren't secret (ports, URLs) — plain
secrets.age   → the secrets: an age file (scrypt passphrase), KEY=value inside

app starts    → sealed.Exists(secrets.age)? → LOCKED: every API call answers 503 "Locked"
-cmd unlock   → reads the passphrase (no echo) → Unix socket inside the container → the app
                decrypts into memory → builds its database, NATS, jobs… → unlocked
```

- **The passphrase is never on the server's disk** — it lives in a password manager.
- **No secrets file → not sealed:** the app reads plain `.env` as before (local development).
- **Wrong passphrases are slowed down:** 2 s after the first, doubling each time, up to 5 min.
- **Standard age format:** `age -d secrets.age` opens it anywhere, for disaster recovery.
- **Protects** a copied folder, a backup, a stolen disk, `docker inspect`. **Not** someone with root
  on the running server (they can read memory).

## Using it in an app

```go
lock := sealed.NewLock("/app/secrets.age", sealed.DefaultSocket)
if sealed.Exists("/app/secrets.age") {
    srv.Handler = lock.Middleware(router, "/lock-status") // 503 until unlocked
    mux.Handle("/lock-status", lock.StatusHandler())      // {"locked": true|false} for the frontend
    go lock.Listen(ctx)                                    // the unlock socket
    secrets, err := lock.Wait(ctx)                         // blocks until unlocked
    sealed.Apply(secrets)                                  // into the process env, memory only
}
cfg := config.Load()                                       // reads the secrets as before
// … now build the database, NATS, encryption, background jobs
```

The app's CLI (`-cmd …`) wraps the rest:

| Command | Calls |
|---|---|
| unlock | `ReadPassphrase` → `SendUnlock` |
| secrets-init | `ReadPassphrase(…, confirm)` → `InitFromEnv` — moves the named keys out of `.env` |
| secrets-edit | `EditFile` — plain copy in `/dev/shm` for the editor, wiped after |
| secrets-passphrase | `ChangePassphrase` |

## Versions

| Version | Change |
|---|---|
| v0.1.0 | First: age file tools, the lock (socket, slowed wrong tries, middleware, status), the unlock client. |
