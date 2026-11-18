{{- /* One finding per line. The secret is redacted. */ -}}
{{- range . }}{{ .File }}:{{ .StartLine }}: [{{ .RuleID }}] {{ trimSuffix "." .Description }}. Fix: remove the secret, rotate it, and load it from the environment at run time
{{ end -}}
