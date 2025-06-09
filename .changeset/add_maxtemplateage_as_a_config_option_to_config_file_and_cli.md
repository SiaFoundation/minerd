---
default: minor
---

# Add MaxTemplateAge as a config option to config file and CLI.

This allows for limiting the age of templates. When the max age is set to a
value > 0, templates will be invalidated once they reach the specified age.
