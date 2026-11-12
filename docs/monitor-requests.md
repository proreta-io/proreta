# Monitor requests

An HTTP monitor calls its URL once a minute. By default that is a plain `GET`. The monitor form also lets you choose the method and add headers and a body, for endpoints that need a token or a JSON `POST`.

## Method
`GET` (default), `HEAD`, `POST`, `PUT`, `PATCH` and `DELETE`. Nothing else.

`POST`, `PUT`, `PATCH` and `DELETE` act on the target system every minute. The form warns for them; use them only against endpoints that are safe to call that often. A failed check is not retried beyond the monitor's "alert after" setting.

## Headers
Up to 10 custom headers, each a name and a value.

- Refused: `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`, `TE`, `Trailer`, `Expect` and every `Proxy-*` header.
- `User-Agent` replaces the default `Proreta/1 (+uptime check)`.
- A name may appear once, in any letter case. Values may not contain line breaks or other control characters.

## Body
Text up to 64 KiB, for `POST`, `PUT`, `PATCH` and `DELETE`. It is sent as is. When the body is set and no `Content-Type` header is given, `application/json` is used. A `GET` or `HEAD` monitor cannot have a body.

## What is stored, and what is never shown
Header values and the body can hold tokens, so they are write-only:

- They are encrypted before they are stored (AES-256-GCM, bound to the organization and the monitor) with the keys in the Secret `<release>-request`, and are never returned by the API, written to logs, audit rows or check results.
- The monitor page shows the method, the header **names** and whether a body is set.
- When you edit a monitor, a saved header keeps its value if you leave the value empty, and the body is kept unless you replace it. Leave the new body empty to remove it.
- The audit log records which fields changed (`http.headers`, `http.body`, `http.method`), never their content.

Installations that have no request keys (the Secret `<release>-request`, see the [chart README](../deploy/helm/proreta/README.md)) accept a method but refuse headers and a body with an error on the form.

## Redirects
A check with custom headers or a body follows redirects only on the same host. A redirect to another host ends the check with the error kind `redirect`, so a token or a body is never sent to a third party. A plain `GET` without headers or body keeps following redirects (up to three, HTTPS only) as before. Every hop goes through the same address checks as the first request.

## Back up the key
The request key is generated in the cluster like the other Secrets; see [Back up the secrets](install.md#back-up-the-secrets). If it is lost, the stored header values and bodies cannot be decrypted: the affected monitors fail their checks until you enter the values again.
