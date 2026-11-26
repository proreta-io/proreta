# TCP monitors

A TCP monitor connects to a host and a port once a minute and is **up** when the TCP handshake completes within 10 seconds. By default nothing is sent and nothing is read: it tells you that something accepts connections on that port, not that the service behind it works. To check that the service answers, add [send and expect](#check-the-reply). Use it for databases, mail servers, message brokers and other services that are not web pages. For a web page, use an HTTP monitor.

## Create one
Choose **TCP port** when you add a monitor, then enter the host (a name or an IP address, without `http://` or a port) and the port. Through the API: `type` `tcp` with `tcp.host` and `tcp.port` (see `api/openapi.yaml`). The check interval (60 s) and the timeout (10 s) are fixed, as for HTTP monitors; "alert after" (1 to 3 failed checks in a row) works the same.

A TCP monitor counts toward the same plan limit as an HTTP monitor.

## Check the reply
Optionally the check writes some bytes after it connects and waits for a reply that matches a rule. This is for services that do not speak HTTP, such as a message transport with its own framing. A monitor without these fields behaves as described above.

| Field | Meaning |
|---|---|
| `tcp.send` | `{encoding, value}`: the bytes to write, `encoding` is `text`, `hex` or `base64`; at most 4096 bytes once decoded. Write-only, like an HTTP request body: stored encrypted, never shown again (the API returns `sendSet: true`). An empty value removes it. |
| `tcp.expect` | `{mode, value}`: `contains` (a substring) or `regex` (Go RE2 syntax, checked when you save); at most 512 characters. Shown as saved. |
| `tcp.readTimeoutSeconds` | How long to wait for the reply, 1 to 10, default 5. Separate from the connect timeout. |
| `tcp.maxResponseBytes` | How much of the reply to read, 1 to 65536, default 4096. |

`send` needs `expect` (a write that is never checked proves little). `expect` alone is allowed and checks a banner the service sends on connect. The check reads until the rule matches, the size limit is reached, the peer closes, or the reply timeout passes. The response time is measured up to the first match, so it includes the time the service takes to answer. The reply itself is never stored or shown.

If you change the host or port, send the `send` value again (or remove it): a stored value is hidden, so it is not carried over to a new target.

Example: a service that expects a frame made of a length, a colon and a JSON body, and answers with JSON. Send the frame as `text` (the body `{"pattern":"health","data":{}}` is 30 bytes, so the frame is `30:` followed by the body; use your protocol's own framing) or as `hex`, and expect `"status":"ok"`:

```json
{
  "type": "tcp",
  "tcp": {
    "host": "app.example.com",
    "port": 4000,
    "send": { "encoding": "text", "value": "30:{\"pattern\":\"health\",\"data\":{}}" },
    "expect": { "mode": "contains", "value": "\"status\":\"ok\"" },
    "readTimeoutSeconds": 5
  }
}
```

Use `hex` or `base64` for binary framing, for example a four-byte length prefix.

## What a check records
- **Up:** the handshake completed. The response time is the time to connect.
- **Down**, with the reason: `dns` (the name did not resolve), `connect` (refused, or no route), `timeout` (no answer within 10 s), `blocked_address` (see below). With send or expect set, also `no_response` (the connection stayed open but no byte arrived before the reply timeout, or the peer closed without sending anything) and `unexpected_response` (bytes arrived, none matched the rule, including when the size limit was reached before a match).
- The reply is never recorded. There is no status code, no TLS information and no request method, headers or body.

Incidents, uptime, response-time charts, notifications and status pages work as for HTTP monitors. In a Slack message the target is shown as `tcp://host:port`.

## From Proreta's cloud
The host must be public. Private and internal addresses and names (`10.x`, `192.168.x`, `localhost`, single-label names, cloud metadata addresses and the like) are refused when you save, and again when the connection is made: the name is resolved at check time and every address must be public, so a public name that points inside is a `blocked_address` failure. Any port from 1 to 65535 is allowed.

## Inside your network
To check an internal service, run the monitor at a [Specula](runbooks/specula.md): choose it under **Run from**. There the host may be any name or address your network resolves, such as `postgres.prod.svc` or `10.0.4.12`, and the port is yours to choose.

Upgrade your Specula executors to this release before you move or create TCP monitors on them: an older executor does not know the TCP check and reports it as a failure. A monitor with an expect rule is handed only to an executor that announces the `tcp/probe` capability (this release and later); until you upgrade, such a monitor shows *unknown* instead of a false *up*. See [Specula](runbooks/specula.md).
