---
name: Bug report
about: Report a Reconify CLI or engine bug
title: ''
labels: 'bug'
assignees: ''

---

**Describe the bug**
A clear and concise description of what the bug is.

**Reconify version**
Output of `reconify --version`.

**Environment**
- Go version (`go version`):
- OS and architecture (e.g. `macOS 14 arm64`, `Ubuntu 22.04 amd64`):

**To reproduce**
The exact command that was run, plus the config snippet it used (with sensitive values removed):

```yaml
# config snippet
```

```sh
reconify <args>
```

**Actual output**
The error message or output you got.

**Expected output**
What you expected to happen instead.

**Additional context**
Anything else relevant. If you are opening a PR with a fix, confirm `make check` passes:

- [ ] `make check` passes with my fix