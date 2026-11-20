{{- .Title | replaceRE "\n" " " | printf "# %s" }}
{{ partial "jackstay/markdown-links.html" (dict "page" .) }}
