{{/* image reference helpers */}}
{{- define "extAuthz.image.echoServer" -}}
{{ .Values.images.echoServer.repository }}:{{ .Values.images.echoServer.tag }}
{{- end -}}

{{- define "extAuthz.image.pcs" -}}
{{ .Values.images.pcs.repository }}:{{ .Values.images.pcs.tag }}
{{- end -}}

{{- define "extAuthz.image.dashboardClient" -}}
{{ .Values.images.dashboardClient.repository }}:{{ .Values.images.dashboardClient.tag }}
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
