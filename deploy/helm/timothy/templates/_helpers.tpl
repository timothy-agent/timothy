{{/*
Naming and shared snippets. Every workload is "<release>-<service>".
*/}}
{{- define "timothy.name" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "timothy.svc" -}}
{{- printf "%s-%s" .root.Release.Name .svc | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "timothy.labels" -}}
app.kubernetes.io/name: timothy
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ include "timothy.tag" . | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "timothy.selector" -}}
app.kubernetes.io/name: timothy
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .svc }}
{{- end -}}

{{- define "timothy.tag" -}}
{{- default .Chart.AppVersion .Values.image.tag -}}
{{- end -}}

{{/* image: Timothy's own services; registry/repository:tag or @digest. */}}
{{- define "timothy.image" -}}
{{- $img := index .root.Values .svc "image" -}}
{{- $tag := default (include "timothy.tag" .root) $img.tag -}}
{{- if $img.digest -}}
{{ .root.Values.image.registry }}/{{ $img.repository }}@{{ $img.digest }}
{{- else -}}
{{ .root.Values.image.registry }}/{{ $img.repository }}:{{ $tag }}
{{- end -}}
{{- end -}}

{{- define "timothy.sandboxImage" -}}
{{- $img := .Values.sandbox.image -}}
{{- if $img.digest -}}
{{ .Values.image.registry }}/{{ $img.repository }}@{{ $img.digest }}
{{- else -}}
{{ .Values.image.registry }}/{{ $img.repository }}:{{ default (include "timothy.tag" .) $img.tag }}
{{- end -}}
{{- end -}}

{{- define "timothy.secretName" -}}
{{- default (printf "%s-secrets" .Release.Name) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "timothy.sandboxNamespace" -}}
{{- default .Release.Namespace .Values.sandbox.namespace -}}
{{- end -}}

{{- define "timothy.workspaceClaim" -}}
{{- default (printf "%s-workspace" .Release.Name) .Values.storage.workspace.existingClaim -}}
{{- end -}}

{{/* The workspace claim a sandbox pod mounts: the same claim in the
release namespace, or the operator's claim in a separate one. */}}
{{- define "timothy.sandboxWorkspaceClaim" -}}
{{- if .Values.sandbox.namespace -}}
{{- required "storage.workspace.sandboxExistingClaim is required when sandbox.namespace is set: a PVC cannot be mounted across namespaces" .Values.storage.workspace.sandboxExistingClaim -}}
{{- else -}}
{{- include "timothy.workspaceClaim" . -}}
{{- end -}}
{{- end -}}

{{- define "timothy.optionalClaim" -}}
{{- $s := index .root.Values.storage .key -}}
{{- if $s.enabled -}}
{{- default (printf "%s-%s" .root.Release.Name .name) $s.existingClaim -}}
{{- end -}}
{{- end -}}

{{/* Pod-level hardening every Timothy pod carries. uid is the image's
own non-root user. */}}
{{- define "timothy.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .uid }}
fsGroup: {{ .uid }}
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "timothy.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{- define "timothy.metricsAnnotations" -}}
{{- if .root.Values.metrics.annotations }}
prometheus.io/scrape: "true"
prometheus.io/port: {{ .port | quote }}
prometheus.io/path: /metrics
{{- end }}
{{- end -}}

{{- define "timothy.pullSecrets" -}}
{{- with .Values.image.pullSecrets }}
imagePullSecrets:
{{- range . }}
  - name: {{ . }}
{{- end }}
{{- end }}
{{- end -}}

{{/* DATABASE_URL: composed from the in-cluster password, or read from
the Secret for an external database. */}}
{{- define "timothy.databaseEnv" -}}
{{- if .Values.postgres.enabled }}
- name: POSTGRES_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "timothy.secretName" . }}
      key: POSTGRES_PASSWORD
- name: DATABASE_URL
  value: postgres://timothy:$(POSTGRES_PASSWORD)@{{ .Release.Name }}-postgres:5432/timothy
{{- else }}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "timothy.secretName" . }}
      key: DATABASE_URL
{{- end }}
{{- end -}}

{{/* NetworkPolicy fragments. */}}
{{- define "timothy.np.dnsEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          {{- toYaml .namespaceSelector | nindent 10 }}
      podSelector:
        matchLabels:
          {{- toYaml .podSelector | nindent 10 }}
  ports:
    - {protocol: UDP, port: 53}
    - {protocol: TCP, port: 53}
{{- end -}}

{{- define "timothy.np.internetEgress" -}}
- to:
    - ipBlock:
        cidr: 0.0.0.0/0
        except:
          {{- toYaml .except | nindent 10 }}
  ports:
    - {protocol: TCP, port: 443}
{{- end -}}

{{- define "timothy.np.peer" -}}
podSelector:
  matchLabels:
    {{- include "timothy.selector" . | nindent 4 }}
{{- end -}}
