{{/*
Common labels applied to every object, so `kubectl get all -l
app.kubernetes.io/part-of=pulsetrace` and helm ownership both work.
*/}}
{{- define "pulsetrace.labels" -}}
app.kubernetes.io/part-of: pulsetrace
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/*
Fully-qualified image reference for a service: <registry><image>:<tag>.
Call as: include "pulsetrace.image" (dict "root" $ "svc" $svc)
*/}}
{{- define "pulsetrace.image" -}}
{{- $root := .root -}}
{{- printf "%s%s:%s" $root.Values.image.registry .svc.image $root.Values.image.tag -}}
{{- end -}}
