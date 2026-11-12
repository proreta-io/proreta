# HTTP monitors

An HTTP monitor calls one HTTPS URL every minute (10 s deadline) and records the status code, the response time and the certificate expiry. The check passes when the status matches the expected status: `2xx` (any 200-299) or one exact code from 200-299 or 400-599. The response body is never read, stored or shown.

## Request method, body and content type

| Field (`http.` in the API) | Values | Default |
|---|---|---|
| `method` | `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE` | `GET` |
| `requestBody` | UTF-8 text, at most 16384 bytes, no NUL characters. Only with `POST`, `PUT`, `PATCH` or `DELETE`. Empty means no body. | empty |
| `contentType` | `application/json`, `text/plain`, `application/x-www-form-urlencoded`, `application/xml` | `application/json` when a body is set |

The check sends the method, the body, `Content-Type` and the computed `Content-Length`, plus the `User-Agent`. There are no other custom headers. A body with `GET` or `HEAD`, an unknown method or content type, or a body that is too large or not valid text is refused with a 422 on the field. A content type without a body is ignored. Heartbeat monitors have none of these fields.

Pick the method and body so that one check is harmless to repeat: it runs every minute from the probe.

### Do not put secrets in the body

The body is stored as plain text and is visible to everyone who can read the monitor, in the web app and through the API (the monitor list only says whether a body is set; the body itself is returned with one monitor). Audit records of a change name the changed fields (`http.requestBody`) and never contain the body, and the body is never written to logs, check results or metrics. Even so, treat it as public inside the organization: tokens and passwords do not belong in it.

### Redirects

Up to three redirects are followed, and every hop is checked again like the first request (HTTPS only, port 443, public addresses only).

- `301`, `302` and `303`: the follow-up is a `GET` without a body, as in a browser.
- `307` and `308`: the method and body are sent again, but only to the same host name. A `307` or `308` that points at another host while the method is not `GET` or `HEAD` fails the check with the error kind `redirect`, so a body never reaches a host that was not entered.

## SSRF protection

The request goes through the same guarded client as before: the address is validated where the connection is made, private and internal ranges are blocked, no proxy from the environment is used, and redirects are validated per hop. Sending a body does not relax any of this.
