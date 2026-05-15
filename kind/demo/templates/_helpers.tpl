{{/*
  Image reference helpers.
  All three return a fully-qualified image string sourced from kind/demo/values.yaml
  under images.* — that file is the single point to swap container registries for
  company / private-registry use.
*/}}
{{- define "extAuthz.image.echoServer" -}}
{{ .Values.images.echoServer }}
{{- end -}}

{{- define "extAuthz.image.pcs" -}}
{{ .Values.images.pcs }}
{{- end -}}

{{- define "extAuthz.image.dashboardClient" -}}
{{ .Values.images.dashboardClient }}
{{- end -}}

{{/* sidecar resource annotations (DRY across opt-in workloads) */}}
{{- define "extAuthz.sidecarAnnotations" -}}
sidecar.istio.io/proxyCPU: {{ .Values.resources.sidecar.proxyCPU | quote }}
sidecar.istio.io/proxyMemory: {{ .Values.resources.sidecar.proxyMemory | quote }}
sidecar.istio.io/proxyCPULimit: {{ .Values.resources.sidecar.proxyCPULimit | quote }}
sidecar.istio.io/proxyMemoryLimit: {{ .Values.resources.sidecar.proxyMemoryLimit | quote }}
{{- end -}}

{{/* opt-in label (DRY across opt-in workloads) */}}
{{- define "extAuthz.optInLabel" -}}
{{ .Values.extAuthz.optInLabel.key }}: {{ .Values.extAuthz.optInLabel.value | quote }}
{{- end -}}
