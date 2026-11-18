{{- /* One finding per line: where, which rule, how bad, and how to fix it. */ -}}
{{- range . }}{{ $target := .Target }}
{{- range .Misconfigurations }}{{ $target }}:{{ with .CauseMetadata.StartLine }}{{ . }}{{ else }}1{{ end }}: [{{ .ID }}] {{ .Severity }} {{ trimSuffix "." .Title }}. Fix: {{ .Resolution }} ({{ .PrimaryURL }})
{{ end }}
{{- range .Vulnerabilities }}{{ $target }}: [{{ .VulnerabilityID }}] {{ .Severity }} {{ .PkgName }} {{ .InstalledVersion }}. Fix: upgrade to {{ if .FixedVersion }}{{ .FixedVersion }}{{ else }}a version without the vulnerability (none released yet){{ end }} ({{ .PrimaryURL }})
{{ end }}
{{- range .Licenses }}{{ $target }}: [license] {{ .Severity }} {{ .PkgName }} is under {{ .Name }}, which trivy.yaml forbids. Fix: replace the dependency (ADR-0006)
{{ end }}
{{- end -}}
