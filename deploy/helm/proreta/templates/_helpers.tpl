{{/* Chart name. */}}
{{- define "proreta.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified base name. Release "proreta" -> "proreta". */}}
{{- define "proreta.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/* Per-component resource name: <fullname>-<component>. Args: dict ctx, component */}}
{{- define "proreta.componentName" -}}
{{- printf "%s-%s" (include "proreta.fullname" .ctx) .component | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Backend image tag. A chart packaged by the release workflow carries the release as appVersion and
uses it by default; the chart in a git checkout has the placeholder appVersion 0.0.0, so image.tag
is required there.
*/}}
{{- define "proreta.imageTag" -}}
{{- $appVersion := ternary "" .Chart.AppVersion (eq .Chart.AppVersion "0.0.0") }}
{{- required "image.tag is required when installing from a git checkout: a release version (X.Y.Z) or a main-branch build (sha-<7 hex>)" (.Values.image.tag | default $appVersion) }}
{{- end }}

{{/* Backend image reference. */}}
{{- define "proreta.image" -}}
{{- printf "%s:%s" .Values.image.repository (include "proreta.imageTag" .) }}
{{- end }}

{{/* Web image reference (tag falls back to image.tag). */}}
{{- define "proreta.webImage" -}}
{{- printf "%s:%s" .Values.web.image.repository (default (include "proreta.imageTag" .) .Values.web.image.tag) }}
{{- end }}

{{/* Selector labels. Args: dict ctx, component */}}
{{- define "proreta.selectorLabels" -}}
app.kubernetes.io/name: {{ include "proreta.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* Common labels. Args: dict ctx, component */}}
{{- define "proreta.labels" -}}
{{ include "proreta.selectorLabels" . }}
app.kubernetes.io/version: {{ include "proreta.imageTag" .ctx | trunc 63 | trimSuffix "-" | quote }}
app.kubernetes.io/managed-by: {{ .ctx.Release.Service }}
{{- end }}

{{/* Secret names. Arg: root context */}}
{{/*
Database Secret (database.secretName, default <fullname>-db). Not managed by Helm/ArgoCD: the
database bootstrap Job creates it at runtime. Its keys are a fixed contract between that Job and the
workloads, not values:
  api_url         api
  worker_url      worker and jobs (jobs claims through SECURITY DEFINER functions, no own role)
  statuspage_url  statuspage
  owner_url       the migrate Job only
plus owner_password, api_password, worker_password, statuspage_password.
*/}}
{{- define "proreta.secret.db" -}}{{ .Values.database.secretName | default (printf "%s-db" (include "proreta.fullname" .)) }}{{- end }}
{{- define "proreta.secret.dbAdmin" -}}{{ include "proreta.secret.db" . }}-admin{{- end }}
{{- define "proreta.secret.admin" -}}{{ include "proreta.fullname" . }}-admin{{- end }}
{{/* One-time plaintext admin password, created by the secrets bootstrap Job, never by Helm. */}}
{{- define "proreta.secret.adminInitial" -}}{{ include "proreta.secret.admin" . }}-initial{{- end }}
{{- define "proreta.secret.crypto" -}}{{ include "proreta.fullname" . }}-crypto{{- end }}
{{/* Cookie key of the status page renderer. */}}
{{- define "proreta.secret.statuspage" -}}{{ include "proreta.fullname" . }}-statuspage{{- end }}
{{/* OAuth client credentials: a Secret you created (secrets.oauth.existingSecret) or the ExternalSecret target. */}}
{{- define "proreta.secret.oauth" -}}{{ .Values.secrets.oauth.existingSecret | default (printf "%s-oauth" (include "proreta.fullname" .)) }}{{- end }}

{{/*
Helm hook annotations next to the ArgoCD ones, so a plain `helm install/upgrade` runs the bootstrap
and migration Jobs in the same order as an ArgoCD sync. ArgoCD takes the hook phase and wave from
its own annotations, but it adds the Helm delete policies to its own: under ArgoCD too, the hook
ServiceAccounts, Roles and RoleBindings (hook-succeeded) are deleted after a successful sync. That
is harmless (they are recreated by the next sync) and reduces standing privileges. The Jobs keep
only before-hook-creation, which ArgoCD's BeforeHookCreation already matches.
Args: dict weight, job (true: keep the Job until the next run so its logs stay readable;
ttlSecondsAfterFinished removes it).
*/}}
{{- define "proreta.helmHook" -}}
helm.sh/hook: pre-install,pre-upgrade
helm.sh/hook-weight: {{ .weight | quote }}
helm.sh/hook-delete-policy: {{ ternary "before-hook-creation" "before-hook-creation,hook-succeeded" (.job | default false) }}
{{- end }}

{{/* config.publicUrl, required. Arg: root context */}}
{{- define "proreta.publicUrl" -}}
{{- required "config.publicUrl is required: the https URL users open, without a trailing slash, for example https://proreta.example.com" .Values.config.publicUrl }}
{{- end }}

{{/* HTTPRoute host names as a YAML list: httpRoute.hostnames, or the host of config.publicUrl. Arg: root context */}}
{{- define "proreta.httpRoute.hostnames" -}}
{{- if .Values.httpRoute.hostnames }}
{{- toYaml .Values.httpRoute.hostnames }}
{{- else }}
{{- $host := (urlParse (include "proreta.publicUrl" .)).host | lower }}
{{- list (regexReplaceAll ":[0-9]+$" $host "") | toYaml }}
{{- end }}
{{- end }}

{{/* Gateway name, required when httpRoute.enabled. Arg: root context */}}
{{- define "proreta.httpRoute.gatewayName" -}}
{{- required "httpRoute.parentRef.name is required when httpRoute.enabled is true: the name of your Gateway API Gateway (set httpRoute.parentRef.namespace too when it is not in the release namespace), or set httpRoute.enabled=false and route to the Services yourself" .Values.httpRoute.parentRef.name }}
{{- end }}

{{/* Namespace of the Gateway proxies for the NetworkPolicy. Arg: root context */}}
{{- define "proreta.networkPolicy.gatewayNamespace" -}}
{{- .Values.networkPolicy.gatewayNamespace | default .Values.httpRoute.parentRef.namespace | default .Release.Namespace }}
{{- end }}

{{/* True when any OAuth provider is enabled. Arg: root context */}}
{{- define "proreta.oauth.enabled" -}}
{{- if or .Values.oauth.google.enabled .Values.oauth.github.enabled }}true{{ end }}
{{- end }}

{{/* True when the secrets bootstrap Job is needed (any secrets.*.source is generate). Arg: root context */}}
{{- define "proreta.secrets.generate" -}}
{{- if or (eq .Values.secrets.admin.source "generate") (eq .Values.secrets.crypto.source "generate") (include "proreta.statuspage.generate" .) }}true{{ end }}
{{- end }}

{{/* True when the secrets bootstrap Job must create the status page cookie key. Arg: root context */}}
{{- define "proreta.statuspage.generate" -}}
{{- if and .Values.statuspage.enabled (eq .Values.secrets.statuspage.source "generate") }}true{{ end }}
{{- end }}

{{/* Env shared by the backend Deployments (config.Server + config.Base). Args: dict ctx, dbKey (a fixed key of the database Secret) */}}
{{- define "proreta.env.common" -}}
- name: PRORETA_LOG_LEVEL
  value: {{ .ctx.Values.config.logLevel | quote }}
- name: PRORETA_ADMIN_ADDR
  value: {{ printf ":%d" (int .ctx.Values.config.adminPort) | quote }}
- name: PRORETA_DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.db" .ctx }}
      key: {{ .dbKey }}
{{- end }}

{{/*
Encryption keys (config.Encryption). Only api and jobs include this: the worker never gets the
keys. Arg: root context
*/}}
{{- define "proreta.env.encryption" -}}
- name: PRORETA_ENCRYPTION_KEYS
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.crypto" . }}
      key: keys
- name: PRORETA_ENCRYPTION_ACTIVE_KEY_ID
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.crypto" . }}
      key: active_key_id
{{- end }}

{{/* SSRF guard extra ranges (config.Guard), api/worker/jobs. Optional: rendered only when set. Arg: root context */}}
{{- define "proreta.env.guard" -}}
{{- with .Values.config.blockedCIDRs }}
- name: PRORETA_BLOCKED_CIDRS
  value: {{ join "," . | quote }}
{{- end }}
{{- end }}

{{/* PRORETA_PUBLIC_URL (api, jobs and statuspage). Arg: root context */}}
{{- define "proreta.env.publicUrl" -}}
- name: PRORETA_PUBLIC_URL
  value: {{ include "proreta.publicUrl" . | quote }}
{{- end }}

{{/* api-only env. Arg: root context */}}
{{- define "proreta.env.api" -}}
- name: PRORETA_HTTP_ADDR
  value: {{ printf ":%d" (int .Values.api.httpPort) | quote }}
- name: PRORETA_ADMIN_LOGIN
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.admin" . }}
      key: login
- name: PRORETA_ADMIN_PASSWORD_HASH
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.admin" . }}
      key: password_hash
- name: PRORETA_SESSION_TTL
  value: {{ .Values.config.sessionTTL | quote }}
- name: PRORETA_COOKIE_SECURE
  value: {{ .Values.config.cookieSecure | quote }}
- name: PRORETA_TRUSTED_PROXY_HOPS
  value: {{ .Values.config.trustedProxyHops | quote }}
{{- /* Only an explicit false turns registration off; a missing value keeps the app default (true). */}}
- name: PRORETA_REGISTRATION_ENABLED
  value: {{ ternary "false" "true" (and (kindIs "bool" .Values.config.registrationEnabled) (not .Values.config.registrationEnabled)) | quote }}
{{- $passwordOff := and (kindIs "bool" .Values.config.passwordLoginEnabled) (not .Values.config.passwordLoginEnabled) }}
{{- if and $passwordOff (not (include "proreta.oauth.enabled" .)) }}
{{- fail "config.passwordLoginEnabled=false needs oauth.google.enabled or oauth.github.enabled" }}
{{- end }}
- name: PRORETA_PASSWORD_LOGIN_ENABLED
  value: {{ ternary "false" "true" $passwordOff | quote }}
{{- with .Values.oauth.allowedDomains }}
- name: PRORETA_OAUTH_ALLOWED_DOMAINS
  value: {{ join "," . | quote }}
{{- end }}
{{- if and (include "proreta.oauth.enabled" .) (not .Values.secrets.oauth.existingSecret) (not .Values.externalSecrets.enabled) }}
{{- fail "OAuth needs its client credentials: create a Secret and set secrets.oauth.existingSecret, or enable externalSecrets and set secrets.oauth.remoteKey (docs/runbooks/oauth.md)" }}
{{- end }}
{{- range $p := list "google" "github" }}
{{- if (index $.Values.oauth $p).enabled }}
- name: PRORETA_OAUTH_{{ upper $p }}_CLIENT_ID
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.oauth" $ }}
      key: {{ $p }}_client_id
- name: PRORETA_OAUTH_{{ upper $p }}_CLIENT_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.oauth" $ }}
      key: {{ $p }}_client_secret
{{- end }}
{{- end }}
{{- end }}

{{/* statuspage-only env (config.Statuspage). Arg: root context */}}
{{- define "proreta.env.statuspage" -}}
- name: PRORETA_HTTP_ADDR
  value: {{ printf ":%d" (int .Values.statuspage.httpPort) | quote }}
- name: PRORETA_COOKIE_SECURE
  value: {{ .Values.config.cookieSecure | quote }}
- name: PRORETA_TRUSTED_PROXY_HOPS
  value: {{ .Values.config.trustedProxyHops | quote }}
- name: PRORETA_STATUSPAGE_COOKIE_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "proreta.secret.statuspage" . }}
      key: cookie_key
{{- end }}

{{/* worker-only env (config.Worker). Arg: root context */}}
{{- define "proreta.env.worker" -}}
{{- $w := .Values.worker }}
- name: PRORETA_WORKER_CLAIM_BATCH
  value: {{ $w.claimBatch | quote }}
- name: PRORETA_WORKER_CONCURRENCY
  value: {{ $w.concurrency | quote }}
- name: PRORETA_WORKER_TENANT_CONCURRENCY
  value: {{ $w.tenantConcurrency | quote }}
- name: PRORETA_WORKER_HOST_CONCURRENCY
  value: {{ $w.hostConcurrency | quote }}
- name: PRORETA_WORKER_LOCATION
  value: {{ required "worker.location is required" $w.location | quote }}
{{- end }}

{{/*
Backend Deployment (api / worker / jobs / statuspage). Args: dict
  ctx, component, cfg (component values), env (pre-rendered env list yaml), httpPort (0 = none)
The container runs /app/<component>, the binary of that name in the backend image.
All three expose the admin port; probes hit /healthz and /readyz there.
NOTE: hostNetwork is intentionally never set (see values.yaml worker comment).
*/}}
{{- define "proreta.backendDeployment" -}}
{{- $ctx := .ctx -}}
{{- $name := include "proreta.componentName" (dict "ctx" $ctx "component" .component) -}}
{{- $sel := dict "ctx" $ctx "component" .component -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $name }}
  labels:
    {{- include "proreta.labels" $sel | nindent 4 }}
spec:
  replicas: {{ .cfg.replicas }}
  revisionHistoryLimit: {{ $ctx.Values.revisionHistoryLimit }}
  strategy:
    type: RollingUpdate
    rollingUpdate:
      {{- toYaml $ctx.Values.strategy.rollingUpdate | nindent 6 }}
  selector:
    matchLabels:
      {{- include "proreta.selectorLabels" $sel | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "proreta.labels" $sel | nindent 8 }}
        {{- with $ctx.Values.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- with $ctx.Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
    spec:
      serviceAccountName: {{ $name }}
      automountServiceAccountToken: false
      terminationGracePeriodSeconds: {{ default $ctx.Values.terminationGracePeriodSeconds .cfg.terminationGracePeriodSeconds }}
      {{- with $ctx.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      securityContext:
        {{- toYaml $ctx.Values.podSecurityContext | nindent 8 }}
      containers:
        - name: {{ .component }}
          image: {{ include "proreta.image" $ctx | quote }}
          imagePullPolicy: {{ $ctx.Values.image.pullPolicy }}
          command: [{{ printf "/app/%s" .component | quote }}]
          ports:
            {{- if .httpPort }}
            - name: http
              containerPort: {{ .httpPort }}
              protocol: TCP
            {{- end }}
            - name: admin
              containerPort: {{ $ctx.Values.config.adminPort }}
              protocol: TCP
          env:
            {{- .env | nindent 12 }}
            {{- with .cfg.env }}
            {{- toYaml . | nindent 12 }}
            {{- end }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: admin
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 3
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: admin
            initialDelaySeconds: 3
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
          resources:
            {{- toYaml .cfg.resources | nindent 12 }}
          securityContext:
            {{- toYaml $ctx.Values.securityContext | nindent 12 }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir:
            sizeLimit: 64Mi
      {{- include "proreta.scheduling" $sel | nindent 6 }}
{{- end }}

{{/* nodeSelector / tolerations / affinity / topologySpread. Args: dict ctx, component */}}
{{- define "proreta.scheduling" -}}
{{- $ctx := .ctx -}}
{{- with $ctx.Values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $ctx.Values.tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $ctx.Values.affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- if $ctx.Values.topologySpread.enabled }}
topologySpreadConstraints:
  - maxSkew: {{ $ctx.Values.topologySpread.maxSkew }}
    topologyKey: {{ $ctx.Values.topologySpread.topologyKey }}
    whenUnsatisfiable: {{ $ctx.Values.topologySpread.whenUnsatisfiable }}
    labelSelector:
      matchLabels:
        {{- include "proreta.selectorLabels" . | nindent 8 }}
{{- end }}
{{- end }}
