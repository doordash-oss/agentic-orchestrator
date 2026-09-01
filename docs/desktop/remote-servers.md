# Connecting the Desktop App to a Server on Another Machine

The Agentico desktop app normally runs and supervises its own bundled server
on the same machine. It can also attach to an `agentico` server you run on
another machine — a home lab box, a workstation under your desk, a build
host. This page covers running that server, pasting its connection string
into the app, the network-security expectations, and the recovery story.

## Running the server on the host

On the machine you want to control remotely, start the server bound to a
network-reachable address:

```bash
agentico server --listen 0.0.0.0:8080 --name "my server"
```

`--listen` accepts a concrete address (`10.0.0.5:8080`) or a wildcard
(`0.0.0.0:8080`, `[::]:8080`) that binds all interfaces; a wildcard bind
advertises the machine's primary non-loopback address. Binding to anything
other than a loopback host puts the server into its **network** posture:
plain HTTP plus a bearer token, with strict Origin enforcement. Right after
the `listening at` line, a network-bound server prints exactly this block
(blank line first):

```text
SECURITY NOTICE: this server is reachable over the network with plain HTTP plus a bearer token.
Anyone with the connection string below has full access to this machine's agentic runtime.
Expose it only on a trusted network (prefer a VPN or Tailscale); use SSH tunneling across
untrusted links.
Listening on all interfaces; advertising the primary interface address http://10.9.8.7:8080.
Connection string: agentico://<token>@10.9.8.7:8080?name=my+server
```

(A bind to a single concrete address omits the
`Listening on all interfaces; advertising the primary interface address ...`
line; a loopback-only server prints no notice and no connection string at
all.)

The connection string is one line in the form
`agentico://<token>@<host>:<port>[?name=<name>]`. The token before the `@`
**is** the server's bearer credential — treat the entire string like a
password.

## Pasting the connection string in the app

In the Agentico app:

1. Open **Settings → Servers**.
2. Click **Add Server…** and paste the full connection string, exactly as
   the server printed it.
3. The app probes the server. On success the server appears in the Servers
   list (and in the footer switcher) as a **remote** entry, and the app
   attaches to it.

Details of the paste flow:

- **Paste-and-probe:** the string crosses from the UI to the main process
  exactly once. The app checks the server's health and compatibility, then
  verifies the token with one authenticated call before persisting anything.
  Any failure (unreachable host, incompatible build, rejected token) saves
  nothing.
- **Token storage:** after a successful probe, the token is encrypted with
  the OS keychain and written as a ciphertext blob next to
  `settings.json`; the settings file itself only ever holds the server's
  name, address-derived key, and timestamps — never the token.
- **Keychain unavailable:** when the OS keychain is unavailable, the app
  tells you the server is kept **for this session only** — it works until
  you quit, and you will need to paste the string again next launch.
- **Already a local server:** if the pasted address turns out to belong to a
  server the app already knows as a *local* entry (matched by the server's
  runtime identity, not its address), the app doesn't add a duplicate — it
  steers you to the existing local entry.

## Adding by deep link

The connection string is itself an `agentico://` URL, so opening it as a
link (for example `open 'agentico://<token>@10.9.8.7:8080?name=my+server'`
on macOS, or clicking it wherever your OS treats it as a link) hands it to a
running Agentico app, which runs the exact same add flow as the paste form:

- On success the server is added (or, if the address turns out to be an
  already-known server, the existing entry is used), the app switches to it,
  and **Settings → Servers** opens showing the entry.
- On failure (unreachable, incompatible, rejected token) nothing is saved;
  the app shows a notification with the same error copy as the add form and
  opens **Settings → Servers** with the form focused so you can re-paste.
- When the OS keychain is unavailable the app still connects for this
  session only and a notification says nothing was saved, matching the
  paste flow.

The link is treated with the same care as a paste: the embedded token is
never logged or echoed anywhere. Only links that parse as a full connection
string (token before the `@`, explicit port) take this path; the app's other
`agentico://` deep links are unaffected.

Deep-link add is reliable while the app is already running (warm start). A
link that has to launch the app first is delivered best-effort: on macOS the
launching URL can be dropped before the app is ready, and on any platform
the result surface may not appear until the app has finished starting up. If
nothing happens on a cold start, open the running app and click the link
again — or paste the string into **Settings → Servers**.

## Network expectations

- Hold connection strings to **trusted networks** — your LAN, a VPN, or
  Tailscale. Anyone who obtains the string has full access to the host's
  agentic runtime: it is a complete credential, not a reference to one.
- Never post the string in chats, tickets, or docs. Rotating it means
  restarting the host server (a new token is generated per server run).
- Prefer binding a specific address (`--listen 10.0.0.5:8080`) when the host
  has several interfaces.

### SSH tunnel alternative

If the link between the machines is untrusted (public Wi-Fi, the open
internet), don't expose the network bind at all. Run the server on the host
with its default loopback bind and tunnel to it:

```bash
# Host: default loopback server
agentico server

# Client machine:
ssh -N -L 18080:127.0.0.1:8080 user@host
```

Then paste a connection string with the loopback address and the host's
token, e.g. `agentico://<token>@127.0.0.1:18080`. Loopback strings paste
fine — the app treats the tunnel endpoint on your own machine like any other
remote server. The token for the host's loopback server lives in its
owner-only discovery file at `~/.agentic-orchestrator/.agentico-server.json`
(`auth_token`); read it on the host and keep the file private, since the
loopback bind prints no connection string.

## Recovery and removal

The app stores remote tokens encrypted with the OS keychain. If the keychain
is wiped, reset, or the OS user changes, the stored blob can no longer be
decrypted:

- The server entry stays in **Settings → Servers**, but attaching lands on
  an error telling you a **re-paste is required**.
- Fix: paste the server's connection string again in **Settings → Servers**.
  The re-paste overwrites the undecryptable blob with a fresh one — no
  manual cleanup on disk is needed.
- **Removing** a server from the Servers list also deletes its token blob;
  nothing of the credential is left behind.
