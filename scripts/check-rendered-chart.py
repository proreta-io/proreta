#!/usr/bin/env python3
"""Asserts security and configuration invariants on a rendered Proreta chart.

Usage: check-rendered-chart.py rendered.yaml [more.yaml ...]
Exits 1 and prints every violated invariant. Needs PyYAML.
"""
import re
import sys

import yaml

ENV_NAMES_ENCRYPTION = {"PRORETA_ENCRYPTION_KEYS", "PRORETA_ENCRYPTION_ACTIVE_KEY_ID"}
ADMIN_ENV = {"PRORETA_ADMIN_LOGIN", "PRORETA_ADMIN_PASSWORD_HASH"}
# The api reads the admin Secret by these keys; the ExternalSecret (externalSecret mode) must map exactly them.
ADMIN_SECRET_KEYS = {"PRORETA_ADMIN_LOGIN": "login", "PRORETA_ADMIN_PASSWORD_HASH": "password_hash"}
# The worker is the only process that runs tenant-supplied targets; the processes below must
# not see anything the worker has not been given.
ENCRYPTION_COMPONENTS = {"api", "jobs"}
# The only components whose ServiceAccount and pod mount an API token (they create Secrets).
TOKEN_COMPONENTS = {"db-bootstrap", "secrets-bootstrap"}
# The exact Secrets the secrets bootstrap Role may read/update, as suffixes of the chart's full name
# (<fullname>-admin, ...).
SECRETS_BOOTSTRAP_SUFFIXES = ("admin", "admin-initial", "crypto")
# Plus the status page cookie key when the renderer is enabled and the key is generated.
STATUSPAGE_SUFFIX = "statuspage"
# Fixed keys of the database Secret per consumer (the contract with the database bootstrap Job).
DB_URL_KEYS = {"api": "api_url", "worker": "worker_url", "jobs": "worker_url", "statuspage": "statuspage_url", "migrate": "owner_url"}
# The only env the public status page renderer may get (config.Statuspage): no encryption keys, no
# admin credentials, no SSRF guard settings, and only its own database URL key.
STATUSPAGE_ENV = {"PRORETA_LOG_LEVEL", "PRORETA_ADMIN_ADDR", "PRORETA_DATABASE_URL", "PRORETA_PUBLIC_URL",
                  "PRORETA_HTTP_ADDR", "PRORETA_COOKIE_SECURE", "PRORETA_TRUSTED_PROXY_HOPS", "PRORETA_STATUSPAGE_COOKIE_KEY"}


def mem_mi(value):
    m = re.fullmatch(r"(\d+)(Mi|Gi)", str(value))
    if not m:
        raise ValueError(f"unsupported memory quantity {value!r}")
    return int(m.group(1)) * (1024 if m.group(2) == "Gi" else 1)


def pod_spec(doc):
    kind = doc["kind"]
    if kind in ("Deployment", "StatefulSet", "DaemonSet"):
        return doc["spec"]["template"]["spec"]
    if kind == "Job":
        return doc["spec"]["template"]["spec"]
    return None


def component(doc):
    return doc["metadata"].get("labels", {}).get("app.kubernetes.io/component", "")


def check(path):
    errors = []

    def fail(msg):
        errors.append(f"{path}: {msg}")

    with open(path) as f:
        docs = [d for d in yaml.safe_load_all(f) if d]

    kinds = [d["kind"] for d in docs]
    # The chart's full name: every component resource is <fullname>-<component>.
    api_sa = [d["metadata"]["name"] for d in docs if d["kind"] == "ServiceAccount" and component(d) == "api"]
    if len(api_sa) != 1:
        fail("cannot find the api ServiceAccount")
        return errors
    fullname = api_sa[0][: -len("-api")]
    SECRETS_BOOTSTRAP_NAMES = {f"{fullname}-{x}" for x in SECRETS_BOOTSTRAP_SUFFIXES}
    STATUSPAGE_SECRET = f"{fullname}-{STATUSPAGE_SUFFIX}"
    ADMIN_SECRET = f"{fullname}-admin"
    if "Ingress" in kinds:
        fail("Ingress object rendered (Gateway API only)")
    for d in docs:
        if d["kind"] == "Service" and d["spec"].get("type", "ClusterIP") != "ClusterIP":
            fail(f"Service {d['metadata']['name']} is {d['spec']['type']} (traffic enters through the Gateway only)")

    encryption_users, owner_users, token_pods = set(), set(), set()
    deployments = {}
    for d in docs:
        spec = pod_spec(d)
        if spec is None:
            continue
        name, comp = d["metadata"]["name"], component(d)
        if d["kind"] == "Deployment":
            deployments[comp] = d
        for flag in ("hostNetwork", "hostPID", "hostIPC"):
            if spec.get(flag):
                fail(f"{name}: {flag} is set")
        if spec.get("automountServiceAccountToken") is not False:
            token_pods.add(comp)
        if not spec.get("securityContext", {}).get("runAsNonRoot"):
            fail(f"{name}: pod securityContext.runAsNonRoot is not true")
        if spec.get("imagePullSecrets"):
            fail(f"{name}: imagePullSecrets set (the chart must default to none)")
        for c in spec["containers"]:
            sc = c.get("securityContext", {})
            if sc.get("allowPrivilegeEscalation") is not False:
                fail(f"{name}/{c['name']}: allowPrivilegeEscalation is not false")
            if sc.get("readOnlyRootFilesystem") is not True:
                fail(f"{name}/{c['name']}: readOnlyRootFilesystem is not true")
            if sc.get("capabilities", {}).get("drop") != ["ALL"]:
                fail(f"{name}/{c['name']}: capabilities.drop is not [ALL]")
            if sc.get("seccompProfile", {}).get("type") != "RuntimeDefault":
                fail(f"{name}/{c['name']}: seccompProfile is not RuntimeDefault")
            if sc.get("privileged"):
                fail(f"{name}/{c['name']}: privileged")
            env = {e["name"]: e for e in c.get("env", [])}
            if ENV_NAMES_ENCRYPTION & set(env):
                encryption_users.add(comp)
            for e in env.values():
                ref = e.get("valueFrom", {}).get("secretKeyRef", {})
                if ref.get("key") == "owner_url":
                    owner_users.add((d["kind"], comp))
                n = e["name"]
                if re.search(r"PASSWORD|KEYS|DATABASE_URL|CLIENT_SECRET", n) and n != "PRORETA_PASSWORD_LOGIN_ENABLED" and "valueFrom" not in e:
                    fail(f"{name}/{c['name']}: {n} is a literal, not a secretKeyRef")
            res = c.get("resources", {})
            if d["kind"] == "Deployment":
                for kind_ in ("requests", "limits"):
                    for r in ("cpu", "memory"):
                        if kind_ == "limits" and r == "cpu":
                            continue
                        if r not in res.get(kind_, {}):
                            fail(f"{name}/{c['name']}: resources.{kind_}.{r} missing")
                try:
                    ratio = mem_mi(res["limits"]["memory"]) / mem_mi(res["requests"]["memory"])
                    if not 3 <= ratio <= 4:
                        fail(f"{name}/{c['name']}: memory limit is {ratio:g}x the request (want 3-4x)")
                except (KeyError, ValueError) as ex:
                    fail(f"{name}/{c['name']}: cannot compare memory ({ex})")
            if comp in ("api", "worker", "jobs", "statuspage"):
                paths = {p["httpGet"]["path"] for p in (c.get("livenessProbe", {}), c.get("readinessProbe", {})) if "httpGet" in p}
                if paths != {"/healthz", "/readyz"}:
                    fail(f"{name}: probes should use /healthz and /readyz, got {sorted(paths)}")
            if comp == "web":
                for probe in ("livenessProbe", "readinessProbe"):
                    hg = c.get(probe, {}).get("httpGet", {})
                    if hg.get("path") != "/healthz" or hg.get("port") != "http":
                        fail(f"{name}: {probe} must GET /healthz on port http")
                mounts = {m["mountPath"] for m in c.get("volumeMounts", [])}
                if mounts != {"/tmp"}:
                    fail(f"{name}: web should mount only /tmp, got {sorted(mounts)}")

    # Env contract per workload (config.go).
    def env_of(comp):
        d = deployments.get(comp)
        return {e["name"]: e for e in d["spec"]["template"]["spec"]["containers"][0].get("env", [])} if d else {}

    if encryption_users != ENCRYPTION_COMPONENTS:
        fail(f"PRORETA_ENCRYPTION_* present in {sorted(encryption_users)}, want exactly {sorted(ENCRYPTION_COMPONENTS)}")
    worker = env_of("worker")
    if not worker:
        fail("no worker Deployment")
    if ADMIN_ENV & set(worker) or any(n.startswith("PRORETA_ENCRYPTION_") for n in worker):
        fail("worker has admin credentials or encryption keys")
    for n in ("PRORETA_WORKER_CLAIM_BATCH", "PRORETA_WORKER_CONCURRENCY", "PRORETA_WORKER_TENANT_CONCURRENCY", "PRORETA_WORKER_HOST_CONCURRENCY", "PRORETA_WORKER_LOCATION"):
        if n not in worker:
            fail(f"worker lacks {n}")
    api, jobs = env_of("api"), env_of("jobs")
    for n in ("PRORETA_ADMIN_LOGIN", "PRORETA_ADMIN_PASSWORD_HASH", "PRORETA_SESSION_TTL", "PRORETA_COOKIE_SECURE", "PRORETA_TRUSTED_PROXY_HOPS", "PRORETA_PUBLIC_URL", "PRORETA_HTTP_ADDR", "PRORETA_REGISTRATION_ENABLED"):
        if n not in api:
            fail(f"api lacks {n}")
    # The kill switch is parsed with strconv.ParseBool: render exactly "true" or "false".
    if api and api.get("PRORETA_REGISTRATION_ENABLED", {}).get("value") not in ("true", "false"):
        fail(f"api PRORETA_REGISTRATION_ENABLED must be \"true\" or \"false\", got {api.get('PRORETA_REGISTRATION_ENABLED')}")
    for comp, env in (("worker", worker), ("jobs", jobs)):
        if "PRORETA_REGISTRATION_ENABLED" in env:
            fail(f"{comp} has PRORETA_REGISTRATION_ENABLED (api only)")
    # OAuth: the password switch is a boolean, client credentials come from the oauth
    # Secret only, in pairs, and only the api gets them.
    if api and api.get("PRORETA_PASSWORD_LOGIN_ENABLED", {}).get("value") not in ("true", "false"):
        fail(f"api PRORETA_PASSWORD_LOGIN_ENABLED must be \"true\" or \"false\", got {api.get('PRORETA_PASSWORD_LOGIN_ENABLED')}")
    oauth_secrets = set()
    for p in ("GOOGLE", "GITHUB"):
        names = {f"PRORETA_OAUTH_{p}_CLIENT_ID": f"{p.lower()}_client_id", f"PRORETA_OAUTH_{p}_CLIENT_SECRET": f"{p.lower()}_client_secret"}
        present = [n for n in names if n in api]
        if present and len(present) != 2:
            fail(f"api has {present} without its pair")
        for n in present:
            ref = api[n].get("valueFrom", {}).get("secretKeyRef", {})
            if set(ref) != {"name", "key"} or ref["key"] != names[n]:
                fail(f"api {n} must come from <oauth secret>/{names[n]}, got {ref}")
            oauth_secrets.add(ref.get("name"))
    if len(oauth_secrets) > 1:
        fail(f"api reads OAuth credentials from several Secrets: {sorted(oauth_secrets)}")
    # A rendered OAuth ExternalSecret (any name; it maps *_client_id keys) must be the Secret the api reads.
    es_oauth = {d["spec"]["target"]["name"] for d in docs if d["kind"] == "ExternalSecret"
                and any(x["secretKey"].endswith("_client_id") for x in d["spec"].get("data", []))}
    if es_oauth and es_oauth != oauth_secrets:
        fail(f"OAuth ExternalSecret(s) {sorted(es_oauth)} rendered but the api reads {sorted(oauth_secrets)}")
    if api and api.get("PRORETA_PASSWORD_LOGIN_ENABLED", {}).get("value") == "false" and not any(n.endswith("_CLIENT_ID") for n in api):
        fail("password login is off but no OAuth provider is configured")
    for comp in deployments:
        if comp != "api" and any(n.startswith("PRORETA_OAUTH_") or n == "PRORETA_PASSWORD_LOGIN_ENABLED" for n in env_of(comp)):
            fail(f"{comp} has OAuth settings (api only)")
    # Outgoing email: the api only queues, so it never gets the SMTP login; jobs connects, so it
    # alone reads the password, from the SMTP Secret by the key `password`; the worker (tenant
    # targets) and everything else get nothing.
    smtp_names = lambda env: {n for n in env if n.startswith("PRORETA_SMTP_")}
    for comp in deployments:
        if comp not in ("api", "jobs") and smtp_names(env_of(comp)):
            fail(f"{comp} has SMTP settings (api and jobs only)")
    if smtp_names(api):
        for n in ("PRORETA_SMTP_HOST", "PRORETA_SMTP_FROM", "PRORETA_SMTP_TLS"):
            if n not in api or n not in jobs:
                fail(f"email is on but api or jobs lacks {n}")
        for n in smtp_names(api) - {"PRORETA_SMTP_HOST", "PRORETA_SMTP_FROM", "PRORETA_SMTP_TLS", "PRORETA_SMTP_PORT"}:
            fail(f"api has {n} (jobs only: the api never connects to the server)")
        if {n: api[n].get("value") for n in smtp_names(api)} != {n: jobs[n].get("value") for n in smtp_names(api)}:
            fail("api and jobs disagree on the SMTP settings")
    if smtp_names(api) != smtp_names(jobs) - {"PRORETA_SMTP_USERNAME", "PRORETA_SMTP_PASSWORD"}:
        fail(f"api and jobs have different SMTP settings: {sorted(smtp_names(api))} / {sorted(smtp_names(jobs))}")
    if ("PRORETA_SMTP_USERNAME" in jobs) != ("PRORETA_SMTP_PASSWORD" in jobs):
        fail("jobs has only one of PRORETA_SMTP_USERNAME and PRORETA_SMTP_PASSWORD")
    pw = jobs.get("PRORETA_SMTP_PASSWORD")
    smtp_secret = None
    if pw:
        ref = pw.get("valueFrom", {}).get("secretKeyRef", {})
        if set(ref) != {"name", "key"} or ref["key"] != "password":
            fail(f"jobs PRORETA_SMTP_PASSWORD must come from <smtp secret>/password, got {ref}")
        smtp_secret = ref.get("name")
    es_smtp = {d["spec"]["target"]["name"] for d in docs if d["kind"] == "ExternalSecret"
               and {x["secretKey"] for x in d["spec"].get("data", [])} == {"password"}}
    if es_smtp and es_smtp != {smtp_secret}:
        fail(f"SMTP ExternalSecret(s) {sorted(es_smtp)} rendered but jobs reads {smtp_secret}")
    if ADMIN_ENV & set(jobs):
        fail("jobs has admin credentials")
    for n, key in ADMIN_SECRET_KEYS.items():
        ref = api.get(n, {}).get("valueFrom", {}).get("secretKeyRef", {})
        if api and (ref.get("name") != ADMIN_SECRET or ref.get("key") != key):
            fail(f"api {n} must come from secret {ADMIN_SECRET} key {key}, got {ref}")
    for n in ("PRORETA_PUBLIC_URL", "PRORETA_ENCRYPTION_KEYS"):
        if n not in jobs:
            fail(f"jobs lacks {n}")
    if "PRORETA_PUBLIC_URL" in worker:
        fail("worker has PRORETA_PUBLIC_URL")
    for comp, env in (("api", api), ("jobs", jobs)):
        url = env.get("PRORETA_PUBLIC_URL", {}).get("value", "")
        if not url.startswith("https://") or url.endswith("/"):
            fail(f"{comp}: PRORETA_PUBLIC_URL {url!r} must be https without trailing slash")
    for comp, d in deployments.items():
        grace = d["spec"]["template"]["spec"].get("terminationGracePeriodSeconds")
        if comp in ("worker", "jobs") and grace != 45:
            fail(f"{comp}: terminationGracePeriodSeconds is {grace}, want 45")

    # Status page renderer: least privilege.
    sp = env_of("statuspage")
    if sp:
        if set(sp) != STATUSPAGE_ENV:
            fail(f"statuspage env is {sorted(sp)}, want exactly {sorted(STATUSPAGE_ENV)}")
        db_ref = sp.get("PRORETA_DATABASE_URL", {}).get("valueFrom", {}).get("secretKeyRef", {})
        if db_ref.get("key") != "statuspage_url":
            fail(f"statuspage must use the statuspage_url key, got {db_ref}")
        key_ref = sp.get("PRORETA_STATUSPAGE_COOKIE_KEY", {}).get("valueFrom", {}).get("secretKeyRef", {})
        if key_ref != {"name": STATUSPAGE_SECRET, "key": "cookie_key"}:
            fail(f"statuspage cookie key must come from {STATUSPAGE_SECRET}/cookie_key, got {key_ref}")
        url = sp.get("PRORETA_PUBLIC_URL", {}).get("value", "")
        if not url.startswith("https://") or url.endswith("/"):
            fail(f"statuspage: PRORETA_PUBLIC_URL {url!r} must be https without trailing slash")
    for comp, env in (("api", api), ("worker", worker), ("jobs", jobs)):
        if "PRORETA_STATUSPAGE_COOKIE_KEY" in env or env.get("PRORETA_DATABASE_URL", {}).get("valueFrom", {}).get("secretKeyRef", {}).get("key") == "statuspage_url":
            fail(f"{comp} has the status page's credentials")

    # One database Secret: every consumer reads it under its fixed key, and the bootstrap Job writes
    # exactly that Secret and may update nothing else.
    db_names = set()
    for d in docs:
        spec = pod_spec(d)
        if spec is None:
            continue
        comp = component(d)
        for c in spec["containers"]:
            for e in c.get("env", []):
                if e["name"] in ("PRORETA_DATABASE_URL", "PRORETA_MIGRATE_DATABASE_URL"):
                    ref = e.get("valueFrom", {}).get("secretKeyRef", {})
                    db_names.add(ref.get("name"))
                    if ref.get("key") != DB_URL_KEYS.get(comp):
                        fail(f"{d['metadata']['name']}: {e['name']} uses key {ref.get('key')!r}, want {DB_URL_KEYS.get(comp)!r}")
                if e["name"] == "PRORETA_BOOTSTRAP_SECRET_NAME":
                    db_names.add(e.get("value"))
    for d in docs:
        if d["kind"] == "Role" and component(d) == "db-bootstrap":
            for rule in d["rules"]:
                if "resourceNames" in rule:
                    db_names.update(rule["resourceNames"])
    if len(db_names) != 1:
        fail(f"database Secret referenced under several names: {sorted(map(str, db_names))}")

    # Owner URL only for the migrate Job.
    if owner_users != {("Job", "migrate")}:
        fail(f"owner_url used by {sorted(owner_users)}, want only the migrate Job")
    for d in docs:
        spec = pod_spec(d)
        if spec and d["kind"] == "Job" and component(d) == "migrate":
            names = [e["name"] for e in spec["containers"][0]["env"]]
            if "PRORETA_MIGRATE_DATABASE_URL" not in names or "PRORETA_DATABASE_URL" in names:
                fail("migrate Job env must contain PRORETA_MIGRATE_DATABASE_URL only")
        elif spec and "PRORETA_MIGRATE_DATABASE_URL" in str(spec["containers"]):
            fail(f"{d['metadata']['name']} has PRORETA_MIGRATE_DATABASE_URL")

    # Token automount: only the two bootstrap ServiceAccounts and Jobs.
    for d in docs:
        if d["kind"] == "ServiceAccount":
            mounts = d.get("automountServiceAccountToken")
            if mounts is True and component(d) not in TOKEN_COMPONENTS:
                fail(f"ServiceAccount {d['metadata']['name']} automounts a token")
            if mounts is not True and mounts is not False:
                fail(f"ServiceAccount {d['metadata']['name']} does not set automountServiceAccountToken")
            if component(d) in TOKEN_COMPONENTS and mounts is not True:
                fail(f"{component(d)} ServiceAccount must automount its token")
    if token_pods - TOKEN_COMPONENTS:
        fail(f"pods without automountServiceAccountToken=false: {sorted(token_pods - TOKEN_COMPONENTS)}")

    # Secrets bootstrap: narrow Role, no DB access, and exactly one source per Secret.
    sb_jobs = [d for d in docs if d["kind"] == "Job" and component(d) == "secrets-bootstrap"]
    modes = {}
    for d in sb_jobs:
        c = d["spec"]["template"]["spec"]["containers"][0]
        env = {e["name"]: e for e in c.get("env", [])}
        for n, e in env.items():
            if not n.startswith(("PRORETA_SECRETS_", "PRORETA_LOG_LEVEL")):
                fail(f"secrets-bootstrap Job has unexpected env {n}")
            if "valueFrom" in e:
                fail(f"secrets-bootstrap Job reads {n} from a Secret")
        if re.search(r"DATABASE|BOOTSTRAP_|owner_url|api_url|worker_url|-db\b", str(c)):
            fail("secrets-bootstrap Job has database configuration")
        for n in ("ADMIN", "CRYPTO", "STATUSPAGE"):
            modes[n] = env.get(f"PRORETA_SECRETS_{n}_MODE", {}).get("value")
            if modes[n] not in ("generate", "skip"):
                fail(f"secrets-bootstrap Job: PRORETA_SECRETS_{n}_MODE is {modes[n]!r}")
        if modes.get("ADMIN") == "generate" and not env.get("PRORETA_SECRETS_ADMIN_LOGIN", {}).get("value"):
            fail("secrets-bootstrap Job generates the admin without PRORETA_SECRETS_ADMIN_LOGIN")
        if d["spec"]["template"]["spec"].get("automountServiceAccountToken") is not True:
            fail("secrets-bootstrap Job must automount its token")
    if len(sb_jobs) > 1:
        fail("more than one secrets-bootstrap Job")
    if sb_jobs and "generate" not in modes.values():
        fail("secrets-bootstrap Job rendered although no secret is generated")
    for d in docs:
        if d["kind"] == "Role" and component(d) == "secrets-bootstrap":
            for rule in d["rules"]:
                verbs, names = set(rule.get("verbs", [])), rule.get("resourceNames")
                if rule.get("resources") != ["secrets"] or rule.get("apiGroups") != [""]:
                    fail(f"secrets-bootstrap Role grants {rule.get('resources')}")
                elif names is None and verbs != {"create"}:
                    fail(f"secrets-bootstrap Role grants {sorted(verbs)} on all secrets")
                elif names is not None:
                    want = SECRETS_BOOTSTRAP_NAMES | ({STATUSPAGE_SECRET} if modes.get("STATUSPAGE") == "generate" else set())
                    if set(names) != want or len(names) != len(want) or verbs != {"get", "update", "patch"}:
                        fail(f"secrets-bootstrap Role: {sorted(verbs)} on {names}, want get/update/patch on {sorted(want)}")
    has_role = any(d["kind"] == "Role" and component(d) == "secrets-bootstrap" for d in docs)
    if bool(sb_jobs) != has_role:
        fail("secrets-bootstrap Job and Role must be rendered together")
    es_names = {d["metadata"]["name"] for d in docs if d["kind"] == "ExternalSecret"}
    for d in docs:
        if d["kind"] == "ExternalSecret" and d["metadata"]["name"] == ADMIN_SECRET:
            props = {(x["secretKey"], x["remoteRef"].get("property")) for x in d["spec"]["data"]}
            want = {(k, k) for k in ADMIN_SECRET_KEYS.values()}
            if props != want:
                fail(f"ExternalSecret {ADMIN_SECRET} maps {sorted(props)}, want {sorted(want)}")
    pairs = [("ADMIN", ADMIN_SECRET), ("CRYPTO", f"{fullname}-crypto")]
    if sp:
        pairs.append(("STATUSPAGE", STATUSPAGE_SECRET))
    elif modes.get("STATUSPAGE") == "generate" or STATUSPAGE_SECRET in es_names:
        fail(f"{STATUSPAGE_SECRET} rendered although the status page is disabled")
    for mode_key, secret in pairs:
        generated = modes.get(mode_key) == "generate"
        if generated and secret in es_names:
            fail(f"{secret}: generated by the secrets bootstrap Job AND rendered as an ExternalSecret")
        if not generated and secret not in es_names:
            fail(f"{secret}: neither generated nor an ExternalSecret")

    # Web and routing.
    web = "web" in deployments
    routes = [d for d in docs if d["kind"] == "HTTPRoute"]
    for r in routes:
        rules = r["spec"]["rules"]
        by_path = {m["path"]["value"]: (rule, rule["backendRefs"][0]["name"]) for rule in rules for m in rule["matches"]}
        if "/api" not in by_path or not by_path["/api"][1].endswith("-api"):
            fail("HTTPRoute: /api must go to the api")
        elif by_path["/api"][0].get("timeouts", {}).get("request") != "60s":
            fail("HTTPRoute: /api request timeout must be 60s")
        if web and not ("/" in by_path and by_path["/"][1].endswith("-web")):
            fail("HTTPRoute: / must go to web")
        if not web and "/" in by_path:
            fail("HTTPRoute: / routed although web is disabled")
        if sp and not ("/status" in by_path and by_path["/status"][1].endswith("-statuspage")):
            fail("HTTPRoute: /status must go to the statuspage")
        if not sp and "/status" in by_path:
            fail("HTTPRoute: /status routed although the status page is disabled")
        # The api and the renderer never share a path: /api stays with the api, /status with the renderer.
        for path, (_, backend) in by_path.items():
            if backend.endswith("-statuspage") and path != "/status":
                fail(f"HTTPRoute: {path} routed to the statuspage")
    check_hooks(docs, fail)
    check_pod_security_restricted(docs, fail)
    return errors


# Kinds that must run before the workloads, with their Helm hook weight (equal to the ArgoCD wave).
HOOK_WEIGHTS = {("ServiceAccount", "db-bootstrap"): -4, ("Role", "db-bootstrap"): -4, ("RoleBinding", "db-bootstrap"): -4,
                ("ExternalSecret", "db-bootstrap"): -4,
                ("ServiceAccount", "secrets-bootstrap"): -4, ("Role", "secrets-bootstrap"): -4, ("RoleBinding", "secrets-bootstrap"): -4,
                ("Job", "db-bootstrap"): -3, ("Job", "secrets-bootstrap"): -3,
                ("ServiceAccount", "migrate"): -2, ("Job", "migrate"): -1}


def check_hooks(docs, fail):
    """Plain `helm install/upgrade` must run the bootstrap and migrate Jobs before the workloads."""
    for d in docs:
        want = HOOK_WEIGHTS.get((d["kind"], component(d)))
        ann = d["metadata"].get("annotations") or {}
        name = f"{d['kind']} {d['metadata']['name']}"
        if want is None:
            if "helm.sh/hook" in ann:
                fail(f"{name}: unexpected Helm hook")
            continue
        if ann.get("helm.sh/hook") != "pre-install,pre-upgrade":
            fail(f"{name}: helm.sh/hook is {ann.get('helm.sh/hook')!r}, want pre-install,pre-upgrade")
        if ann.get("helm.sh/hook-weight") != str(want):
            fail(f"{name}: helm.sh/hook-weight is {ann.get('helm.sh/hook-weight')!r}, want {want}")
        policy = set((ann.get("helm.sh/hook-delete-policy") or "").split(","))
        # Jobs stay until the next run (logs readable; ttlSecondsAfterFinished removes them).
        if "before-hook-creation" not in policy or (d["kind"] == "Job") == ("hook-succeeded" in policy):
            fail(f"{name}: helm.sh/hook-delete-policy is {sorted(policy)}")
        if "argocd.argoproj.io/hook" not in ann:
            fail(f"{name}: Helm hook without an ArgoCD hook annotation (ArgoCD would map the Helm hook instead)")
        # ArgoCD waves and Helm weights give the same order. Documented exception: with
        # migration.hookPhase=PreSync the migrate objects run in their own phase (SA wave -1, Job no wave).
        wave = ann.get("argocd.argoproj.io/sync-wave")
        presync = component(d) == "migrate" and ann.get("argocd.argoproj.io/hook") == "PreSync"
        if presync:
            if wave != ("-1" if d["kind"] == "ServiceAccount" else None):
                fail(f"{name}: PreSync sync-wave is {wave!r}")
        elif wave != ann.get("helm.sh/hook-weight"):
            fail(f"{name}: argocd.argoproj.io/sync-wave {wave!r} differs from helm.sh/hook-weight {ann.get('helm.sh/hook-weight')!r}")

    # Every hook Job finds the ServiceAccount (and the bootstrap Jobs their Role and RoleBinding) it needs.
    present = {(d["kind"], d["metadata"]["name"]) for d in docs}
    for d in docs:
        if d["kind"] != "Job":
            continue
        sa = d["spec"]["template"]["spec"].get("serviceAccountName")
        need = [("ServiceAccount", sa)]
        if component(d) in TOKEN_COMPONENTS:
            need += [("Role", sa), ("RoleBinding", sa)]
        for kind, n in need:
            if (kind, n) not in present:
                fail(f"Job {d['metadata']['name']}: {kind} {n} is not rendered")


def check_pod_security_restricted(docs, fail):
    """The Pod Security Standard "restricted" profile, for every pod template."""
    allowed_volumes = {"configMap", "csi", "downwardAPI", "emptyDir", "ephemeral", "persistentVolumeClaim", "projected", "secret"}
    for d in docs:
        spec = pod_spec(d)
        if spec is None:
            continue
        name = d["metadata"]["name"]
        psc = spec.get("securityContext", {})
        for v in spec.get("volumes", []):
            kinds = set(v) - {"name"}
            if not kinds <= allowed_volumes:
                fail(f"{name}: volume {v['name']} of type {sorted(kinds)} is not allowed by restricted")
        for c in spec.get("initContainers", []) + spec["containers"]:
            sc = c.get("securityContext", {})
            non_root = sc.get("runAsNonRoot", psc.get("runAsNonRoot"))
            if non_root is not True:
                fail(f"{name}/{c['name']}: runAsNonRoot is not true")
            if sc.get("runAsUser", psc.get("runAsUser")) == 0:
                fail(f"{name}/{c['name']}: runAsUser 0")
            seccomp = sc.get("seccompProfile", psc.get("seccompProfile", {})).get("type")
            if seccomp not in ("RuntimeDefault", "Localhost"):
                fail(f"{name}/{c['name']}: seccompProfile {seccomp!r}")
            if sc.get("allowPrivilegeEscalation") is not False:
                fail(f"{name}/{c['name']}: allowPrivilegeEscalation is not false")
            caps = sc.get("capabilities", {})
            if "ALL" not in caps.get("drop", []) or set(caps.get("add", [])) - {"NET_BIND_SERVICE"}:
                fail(f"{name}/{c['name']}: capabilities {caps}")
            if any(p.get("hostPort") for p in c.get("ports", [])):
                fail(f"{name}/{c['name']}: hostPort")


def main():
    errors = []
    for p in sys.argv[1:]:
        errors += check(p)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"check-rendered-chart: ok ({len(sys.argv) - 1} render(s))")
    return 0


if __name__ == "__main__":
    sys.exit(main())
