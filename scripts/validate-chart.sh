#!/usr/bin/env bash
# Static validation of the Helm chart: helm lint and helm template for every value
# combination we ship, invariants on the rendered YAML, and kubeconform when it is installed.
# Never talks to a cluster. Run from anywhere: bash scripts/validate-chart.sh
#
# Optional environment:
#   CRD_SCHEMA_DIR  directory laid out like datreeio/CRDs-catalog ({group}/{kind}_{version}.json) with
#                   the Gateway API and External Secrets schemas; without it those kinds are skipped.
#   KUBERNETES_VERSION  schema version for kubeconform (default 1.31.0).
set -euo pipefail

cd "$(dirname "$0")/.."
chart=deploy/helm/proreta
example=$chart/values.example.yaml
# image.tag is required; a placeholder for the renders that do not use the example file.
tag="--set image.tag=sha-0000000"
# The chart defaults plus the two values that have none (config.publicUrl, httpRoute.parentRef.name).
min="$tag --set config.publicUrl=https://proreta.example.com --set httpRoute.parentRef.name=gateway"
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

# External Secrets is opt-in; these combos switch it on.
eso_args=(--set externalSecrets.enabled=true --set externalSecrets.secretStoreRef.name=my-secret-store)
eso="${eso_args[*]}"
oauth_secret="--set secrets.oauth.existingSecret=proreta-oauth-clients"

# A chart packaged like the release workflow does (appVersion from the tag), installed without image.tag.
packaged=$out/packaged
mkdir -p "$packaged"
helm package "$chart" --version 1.2.3 --app-version 1.2.3 -d "$packaged" >/dev/null
tar -xzf "$packaged/proreta-1.2.3.tgz" -C "$packaged"

# name|helm args (chart path first when it is not $chart)
combos=(
  "defaults|$min"
  "defaults-url-port|$min --set config.publicUrl=https://Status.Example.org:8443/proreta"
  "defaults-hostnames|$min --set httpRoute.hostnames[0]=proreta.example.com --set httpRoute.hostnames[1]=status.example.com"
  "example|-f $example $tag"
  "example-no-bootstrap|-f $example $tag --set database.bootstrap.enabled=false"
  "example-db-secret-name|-f $example $tag --set database.secretName=my-db"
  "example-existing-db-secret|-f $example $tag --set database.bootstrap.enabled=false --set database.secretName=existing-db"
  "example-gateway-same-namespace|-f $example $tag --set httpRoute.parentRef.namespace= --set networkPolicy.enabled=true"
  "example-gateway-proxy-namespace|-f $example $tag --set networkPolicy.enabled=true --set networkPolicy.gatewayNamespace=gateway-proxies"
  "example-no-web|-f $example $tag --set web.enabled=false"
  "example-login-set|-f $example $tag --set secrets.admin.login=owner+ops@example.com"
  "example-registration-on|-f $example $tag --set config.registrationEnabled=true"
  "example-external-secrets|-f $example $tag $eso --set secrets.admin.source=externalSecret --set secrets.crypto.source=externalSecret"
  "example-admin-external|-f $example $tag $eso --set secrets.admin.source=externalSecret"
  "example-crypto-external|-f $example $tag $eso --set secrets.crypto.source=externalSecret"
  "example-no-statuspage|-f $example $tag --set statuspage.enabled=false"
  "example-statuspage-external|-f $example $tag $eso --set statuspage.enabled=true --set secrets.statuspage.source=externalSecret"
  "defaults-no-statuspage|$min --set statuspage.enabled=false"
  "defaults-no-route|$tag --set config.publicUrl=https://proreta.example.com --set httpRoute.enabled=false"
  "example-eso-db-admin|-f $example $tag $eso --set database.bootstrap.adminSecret.existingSecret="
  "example-oauth|-f $example $tag $oauth_secret --set oauth.google.enabled=true --set oauth.github.enabled=true --set oauth.allowedDomains[0]=example.com --set oauth.allowedDomains[1]=example.org"
  "example-oauth-eso|-f $example $tag $eso --set oauth.google.enabled=true --set oauth.github.enabled=true"
  "example-oauth-only|-f $example $tag $oauth_secret --set oauth.google.enabled=true --set config.passwordLoginEnabled=false"
  "example-all-optional|-f $example $tag --set statuspage.enabled=true --set serviceMonitor.enabled=true --set networkPolicy.enabled=true --set api.pdb.enabled=true --set topologySpread.enabled=true --set config.blockedCIDRs[0]=100.64.0.0/10 --set config.blockedCIDRs[1]=198.18.0.0/15 --set worker.location=eu-west"
  "example-oauth-existing-default-name|-f $example $tag --set secrets.oauth.existingSecret=proreta-oauth --set oauth.google.enabled=true"
  "example-presync|-f $example $tag --set database.bootstrap.enabled=false --set migration.hookPhase=PreSync"
  "example-no-section-name|-f $example $tag --set httpRoute.parentRef.sectionName="
  "packaged-no-tag|chart=$packaged/proreta -f $example"
)

renders=()
for combo in "${combos[@]}"; do
  name=${combo%%|*}
  args=${combo#*|}
  path=$chart
  if [[ $args == chart=* ]]; then
    path=${args%% *}
    path=${path#chart=}
    args=${args#* }
  fi
  echo "== helm lint --strict: $name"
  # shellcheck disable=SC2086  # args are intentionally word-split
  helm lint --strict "$path" $args >"$out/$name.lint" 2>&1 || { cat "$out/$name.lint"; exit 1; }
  tail -n 1 "$out/$name.lint"
  echo "== helm template: $name"
  # shellcheck disable=SC2086
  helm template proreta "$path" -n proreta $args >"$out/$name.yaml"
  renders+=("$out/$name.yaml")
done

echo "== packaged chart defaults image tags to its appVersion"
grep -q 'image: "ghcr.io/proreta-io/proreta-backend:1.2.3"' "$out/packaged-no-tag.yaml" || { echo "FAIL: packaged chart does not use appVersion 1.2.3 for the backend" >&2; exit 1; }
grep -q 'image: "ghcr.io/proreta-io/proreta-web:1.2.3"' "$out/packaged-no-tag.yaml" || { echo "FAIL: packaged chart does not use appVersion 1.2.3 for the web" >&2; exit 1; }
echo ok

echo "== registration kill switch (PRORETA_REGISTRATION_ENABLED, api only)"
kill_switch() { grep -A1 'name: PRORETA_REGISTRATION_ENABLED' "$out/$1.yaml" | sed -n 's/.*value: //p'; }
[ "$(kill_switch defaults)" = '"false"' ] || { echo "FAIL: registration not off by the chart default" >&2; exit 1; }
[ "$(kill_switch example)" = '"false"' ] || { echo "FAIL: registration not off in values.example.yaml" >&2; exit 1; }
[ "$(kill_switch example-registration-on)" = '"true"' ] || { echo "FAIL: config.registrationEnabled=true not rendered" >&2; exit 1; }
echo "chart default \"false\", example \"false\", config.registrationEnabled=true renders \"true\""

echo "== negative checks (rendering must fail)"
# Every negative starts from $min, which renders, so each one fails for its own reason. With
# `--msg <text>` right after the label the error output must also contain that text.
expect_fail() {
  local label=$1 pattern=""
  shift
  if [ "${1:-}" = "--msg" ]; then pattern=$2; shift 2; fi
  # shellcheck disable=SC2086  # $min is intentionally word-split; later flags override it
  if helm template proreta "$chart" -n proreta $min "$@" >"$out/fail.log" 2>&1; then
    echo "FAIL: $label rendered but must be rejected" >&2
    exit 1
  fi
  if [ -n "$pattern" ] && ! grep -qF -- "$pattern" "$out/fail.log"; then
    echo "FAIL: $label rejected without the expected message ($pattern):" >&2
    cat "$out/fail.log" >&2
    exit 1
  fi
  echo "rejected as expected: $label"
}
expect_fail "empty image tag" --set image.tag=
expect_fail "example values without an image tag from a git checkout" -f "$example" --set image.tag=
expect_fail "missing public URL" --msg "config.publicUrl is required" --set config.publicUrl=
expect_fail "missing public URL without a route" --msg "config.publicUrl is required" --set config.publicUrl= --set httpRoute.enabled=false
expect_fail "missing Gateway" --msg "httpRoute.parentRef.name is required" --set httpRoute.parentRef.name=
expect_fail "HTTPRoute without a Gateway name in the example" -f "$example" --set httpRoute.parentRef.name=
expect_fail "typo in a database key" --msg "additional properties 'hots' not allowed" --set database.hots=db.example.com
expect_fail "typo at the top level" --msg "additional properties 'databse' not allowed" --set databse.host=db.example.com
expect_fail "removed key dbBootstrap" --msg "additional properties 'dbBootstrap' not allowed" --set dbBootstrap.enabled=true
expect_fail "removed key web.port" --msg "additional properties 'port' not allowed" --set web.port=8080
expect_fail "removed key api.databaseUrlKey" --msg "additional properties 'databaseUrlKey' not allowed" --set api.databaseUrlKey=owner_url
expect_fail "removed key statuspage.databaseUrlKey" --set statuspage.databaseUrlKey=api_url
expect_fail "removed key worker.command" --msg "additional properties 'command' not allowed" --set "worker.command[0]=/bin/sh"
expect_fail "removed key migration.databaseUrlKey" --set migration.databaseUrlKey=api_url
expect_fail "db bootstrap without External Secrets and without existingSecret" --msg "database.bootstrap needs the database server admin login" -f "$example" --set database.bootstrap.adminSecret.existingSecret=
expect_fail "bootstrap without host" --msg "database.host is required" --set database.bootstrap.enabled=true --set database.bootstrap.adminSecret.existingSecret=db-admin
expect_fail "invalid database Secret name" --set database.secretName=Proreta_DB
expect_fail "OAuth without existingSecret and without External Secrets" -f "$example" --set oauth.github.enabled=true
expect_fail "admin source externalSecret without External Secrets" -f "$example" --set secrets.admin.source=externalSecret
expect_fail "statuspage source externalSecret without External Secrets" -f "$example" --set secrets.statuspage.source=externalSecret
expect_fail "External Secrets without a store" -f "$example" "${eso_args[@]}" --set externalSecrets.secretStoreRef=null
expect_fail "External Secrets without a store name" --msg "externalSecrets.secretStoreRef.name" -f "$example" --set externalSecrets.enabled=true
expect_fail "image tag with a leading v" --set image.tag=v1.2.3
expect_fail "hostname with an inner wildcard" -f "$example" --set "httpRoute.hostnames[0]=a*b.example.com"
expect_fail "http public URL" --set config.publicUrl=http://proreta.example.com
expect_fail "public URL with trailing slash" --set config.publicUrl=https://proreta.example.com/
expect_fail "PreSync migration with bootstrap" -f "$example" --set database.bootstrap.enabled=true --set migration.hookPhase=PreSync
expect_fail "session TTL that is not a duration" --msg "sessionTTL" --set config.sessionTTL=tomorrow
expect_fail "session TTL without a unit" --msg "sessionTTL" --set config.sessionTTL=24
expect_fail "nameOverride that is not a DNS label" --msg "nameOverride" --set nameOverride=Bad_Name
expect_fail "fullnameOverride that is not a DNS label" --msg "fullnameOverride" --set fullnameOverride=-bad
expect_fail "negative api replicas" --msg "replicas" --set api.replicas=-1
expect_fail "memory limit missing" --set worker.resources.limits=null
expect_fail "unknown resource field" --set worker.resources.request.cpu=1
expect_fail "unknown secrets source" --set secrets.admin.source=manual
expect_fail "upper-case admin login" --set secrets.admin.login=Admin
expect_fail "admin login with a space" --set "secrets.admin.login=a b"
expect_fail "admin login shorter than 3" --set secrets.admin.login=ab
expect_fail "registrationEnabled not a boolean" --set-string config.registrationEnabled=yes
expect_fail "empty admin login in generate mode" --set secrets.admin.login=
expect_fail "admin login removed in generate mode" -f "$example" --set secrets.admin.login=null
expect_fail "invalid crypto key id" --set "secrets.crypto.keyId=k 1"
expect_fail "externalSecret without remote key" "${eso_args[@]}" --set secrets.crypto.source=externalSecret --set secrets.crypto.remoteKey=
expect_fail "statuspage cookie key externalSecret without remote key" "${eso_args[@]}" --set secrets.statuspage.source=externalSecret --set secrets.statuspage.remoteKey=
expect_fail "status path moved" --set httpRoute.statusPathPrefix=/pages
expect_fail "password login off without an OAuth provider" --set config.passwordLoginEnabled=false
expect_fail "passwordLoginEnabled not a boolean" --set-string config.passwordLoginEnabled=no
expect_fail "upper-case allowed domain" --set oauth.allowedDomains[0]=Example.com
expect_fail "wildcard allowed domain" --set "oauth.allowedDomains[0]=*.example.com"
expect_fail "oauth without remote key" "${eso_args[@]}" --set oauth.google.enabled=true --set secrets.oauth.remoteKey=
expect_fail "invalid worker location" --set worker.location=Home_1

echo "== defaults: web on, host names from config.publicUrl"
if ! { grep -q '^kind: Deployment' "$out/defaults.yaml" && grep -q 'name: proreta-web$' "$out/defaults.yaml"; }; then echo "FAIL: web is not enabled by default" >&2; exit 1; fi
if grep -q 'name: proreta-web$' "$out/example-no-web.yaml"; then echo "FAIL: web.enabled=false still renders the web" >&2; exit 1; fi
python3 - "$out" <<'EOF'
import sys, yaml
out = sys.argv[1]
def route(name):
    docs = [d for d in yaml.safe_load_all(open(f"{out}/{name}.yaml")) if d]
    r = [d for d in docs if d["kind"] == "HTTPRoute"]
    return r[0]["spec"] if r else None
want = {"defaults": ["proreta.example.com"], "example": ["proreta.example.com"],
        "defaults-url-port": ["status.example.org"],
        "defaults-hostnames": ["proreta.example.com", "status.example.com"]}
for name, hosts in want.items():
    got = route(name)["hostnames"]
    assert got == hosts, f"{name}: hostnames {got}, want {hosts}"
assert route("defaults-no-route") is None, "httpRoute.enabled=false renders an HTTPRoute"
ref = route("example-gateway-same-namespace")["parentRefs"][0]
assert "namespace" not in ref, f"empty parentRef.namespace rendered: {ref}"
print("ok")
EOF

echo "== NetworkPolicy Gateway namespace: explicit, else parentRef.namespace, else the release namespace"
gw_ns() { grep -A3 'namespaceSelector:' "$out/$1.yaml" | sed -n 's/.*kubernetes.io\/metadata.name: //p' | head -n 1; }
[ "$(gw_ns example-all-optional)" = gateway-system ] || { echo "FAIL: gatewayNamespace not derived from parentRef.namespace" >&2; exit 1; }
[ "$(gw_ns example-gateway-same-namespace)" = proreta ] || { echo "FAIL: gatewayNamespace not the release namespace" >&2; exit 1; }
[ "$(gw_ns example-gateway-proxy-namespace)" = gateway-proxies ] || { echo "FAIL: networkPolicy.gatewayNamespace ignored" >&2; exit 1; }
echo ok

echo "== database Secret name: <fullname>-db, database.secretName, two releases in one namespace"
# shellcheck disable=SC2086
helm template second "$chart" -n proreta -f "$example" $tag >"$out/second-release.yaml"
renders+=("$out/second-release.yaml")
python3 - "$out" <<'EOF'
import sys, yaml
out = sys.argv[1]
def db_secrets(name):
    """Every reference to the database Secret: workload and migrate env, bootstrap target and Role."""
    docs = [d for d in yaml.safe_load_all(open(f"{out}/{name}.yaml")) if d]
    refs, bootstrap = set(), False
    for d in docs:
        comp = d["metadata"].get("labels", {}).get("app.kubernetes.io/component")
        if d["kind"] in ("Deployment", "Job"):
            for c in d["spec"]["template"]["spec"]["containers"]:
                for e in c.get("env", []):
                    if e["name"] in ("PRORETA_DATABASE_URL", "PRORETA_MIGRATE_DATABASE_URL"):
                        refs.add(e["valueFrom"]["secretKeyRef"]["name"])
                    if e["name"] == "PRORETA_BOOTSTRAP_SECRET_NAME":
                        refs.add(e["value"]); bootstrap = True
        if d["kind"] == "Role" and comp == "db-bootstrap":
            refs.update(n for r in d["rules"] for n in r.get("resourceNames", []))
    return refs, bootstrap, {(d["kind"], d["metadata"]["name"]) for d in docs}
want = {"example": ({"proreta-db"}, True), "second-release": ({"second-proreta-db"}, True),
        "example-db-secret-name": ({"my-db"}, True), "example-existing-db-secret": ({"existing-db"}, False),
        "example-no-bootstrap": ({"proreta-db"}, False)}
for name, (refs, bootstrap) in want.items():
    got, got_bootstrap, _ = db_secrets(name)
    assert got == refs, f"{name}: database Secret references {sorted(got)}, want {sorted(refs)}"
    assert got_bootstrap == bootstrap, f"{name}: database bootstrap rendered={got_bootstrap}, want {bootstrap}"
shared = db_secrets("example")[2] & db_secrets("second-release")[2]
assert not shared, f"two releases in one namespace share objects: {sorted(shared)}"
print("ok")
EOF

echo "== sectionName omitted when empty"
if grep -q 'sectionName:' "$out/example-no-section-name.yaml"; then echo "FAIL: empty sectionName rendered" >&2; exit 1; fi
grep -q 'sectionName: https' "$out/example.yaml" || { echo "FAIL: sectionName missing in the example render" >&2; exit 1; }
echo ok

echo "== no ExternalSecret without External Secrets (the default path)"
for r in defaults example example-oauth example-oauth-only example-all-optional; do
  if grep -q '^kind: ExternalSecret' "$out/$r.yaml"; then
    echo "FAIL: $r renders an ExternalSecret although externalSecrets.enabled=false" >&2; exit 1
  fi
done
grep -q 'name: proreta-db-admin-login' "$out/example.yaml" || { echo "FAIL: existingSecret not used by the db bootstrap Job" >&2; exit 1; }
grep -q 'name: proreta-oauth-clients' "$out/example-oauth.yaml" || { echo "FAIL: secrets.oauth.existingSecret not used by the api" >&2; exit 1; }
grep -q '^kind: ExternalSecret' "$out/example-eso-db-admin.yaml" || { echo "FAIL: no db admin ExternalSecret with externalSecrets.enabled" >&2; exit 1; }
grep -q 'name: proreta-oauth$' "$out/example-oauth-eso.yaml" || { echo "FAIL: no OAuth ExternalSecret with externalSecrets.enabled" >&2; exit 1; }
echo "ok"

echo "== invariants on rendered manifests"
python3 scripts/check-rendered-chart.py "${renders[@]}"

echo "== kubeconform"
if ! command -v kubeconform >/dev/null 2>&1; then
  echo "kubeconform not installed: skipped (CI installs a pinned release)"
  exit 0
fi
kc=(kubeconform -strict -summary -kubernetes-version "${KUBERNETES_VERSION:-1.31.0}")
if [ -n "${CRD_SCHEMA_DIR:-}" ]; then
  kc+=(-schema-location default -schema-location "$CRD_SCHEMA_DIR/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json")
else
  echo "CRD_SCHEMA_DIR not set: skipping Gateway API (HTTPRoute), External Secrets (ExternalSecret) and Prometheus Operator (ServiceMonitor) kinds"
  kc+=(-skip "HTTPRoute,ExternalSecret,ServiceMonitor")
fi
"${kc[@]}" "${renders[@]}"
