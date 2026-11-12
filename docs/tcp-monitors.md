# TCP monitors

A TCP monitor connects to a host and a port once a minute and is **up** when the TCP handshake completes within 10 seconds. Nothing is sent and nothing is read: it tells you that something accepts connections on that port, not that the service behind it works. Use it for databases, mail servers, message brokers and other services that are not web pages. For a web page, use an HTTP monitor.

## Create one
Choose **TCP port** when you add a monitor, then enter the host (a name or an IP address, without `http://` or a port) and the port. Through the API: `type` `tcp` with `tcp.host` and `tcp.port` (see `api/openapi.yaml`). The check interval (60 s) and the timeout (10 s) are fixed, as for HTTP monitors; "alert after" (1 to 3 failed checks in a row) works the same.

A TCP monitor counts toward the same plan limit as an HTTP monitor.

## What a check records
- **Up:** the handshake completed. The response time is the time to connect.
- **Down**, with the reason: `dns` (the name did not resolve), `connect` (refused, or no route), `timeout` (no answer within 10 s), `blocked_address` (see below).
- There is no status code, no TLS information and no request method, headers or body.

Incidents, uptime, response-time charts, notifications and status pages work as for HTTP monitors. In a Slack message the target is shown as `tcp://host:port`.

## From Proreta's cloud
The host must be public. Private and internal addresses and names (`10.x`, `192.168.x`, `localhost`, single-label names, cloud metadata addresses and the like) are refused when you save, and again when the connection is made: the name is resolved at check time and every address must be public, so a public name that points inside is a `blocked_address` failure. Any port from 1 to 65535 is allowed.

## Inside your network
To check an internal service, run the monitor at a [Specula](runbooks/specula.md): choose it under **Run from**. There the host may be any name or address your network resolves, such as `postgres.prod.svc` or `10.0.4.12`, and the port is yours to choose.

Upgrade your Specula executors to this release before you move or create TCP monitors on them: an older executor does not know the TCP check and reports it as a failure.
