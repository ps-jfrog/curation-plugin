# curation-plugin

A JFrog CLI plugin that downloads Curation audit events (blocked, approved,
not-inspected, dry-run) as a CSV file.

## Prerequisites

- [JFrog CLI](https://jfrog.com/getcli/) installed (`jf --version` to check).
- [Go 1.14 or later](https://go.dev/dl/) installed (only needed to build the plugin, `go version` to check).
- A configured JFrog Platform server with Xray access:
  ```
  jf config add my-server
  jf config use my-server
  jf rt ping
  ```
  `jf rt ping` should print `OK`, confirming the server is reachable.

## Build

From the plugin's source directory:

```bash
go mod tidy
go build -o curation-plugin .
```

`go mod tidy` downloads dependencies and fills in `go.sum`; it's only needed
once (or after changing imports) — skip it on rebuilds if `go.sum` already
exists and `go build` succeeds on its own.

This produces a single binary named `curation-plugin`.

## Install

JFrog CLI loads plugins from `~/.jfrog/plugins/<plugin-name>/bin/<plugin-name>`.
Copy the binary into place:

```bash
mkdir -p ~/.jfrog/plugins/curation-plugin/bin
cp curation-plugin ~/.jfrog/plugins/curation-plugin/bin/
```

Verify it's picked up:

```bash
jf curation-plugin ce --help
```

To upgrade after a code change, rebuild and re-copy the binary over the same path.

## Usage

```
jf curation-plugin ce [options]
```

With no options at all, it downloads the last 7 days of real (non-dry-run)
curation events, unfiltered, to `curation-audit-last-7-days.csv` in the current
directory.

### Options

| Flag | Description |
|---|---|
| `--from` | Start date, `YYYY-MM-DD`. Defaults to 7 days ago if omitted. |
| `--to` | End date, `YYYY-MM-DD`. Defaults to now if omitted. |
| `--status` | Comma-separated filter. Any combination of `blocked`, `approved`, `not-inspected`, `dry-run`. Default (omitted): all real events, unfiltered. |
| `--project` | Only keep rows whose curated repo name starts with `<project>-`. |
| `--output` | Output CSV file path. Defaults to `curation-audit-<from>_<to>.csv` (or `curation-audit-last-7-days.csv` if no dates given). |

### `--status` values explained

| Value | Meaning |
|---|---|
| `blocked` | Package was really blocked by a policy (a real violation, not a simulation). |
| `approved` | Package was checked and served to the consumer with no violation. |
| `not-inspected` | Package just arrived into Artifactory's cache from the upstream registry; not yet policy-checked. |
| `dry-run` | Events from a policy running in test/simulation mode — shows what it *would* have blocked, without actually enforcing anything. |

Any date range longer than 7 days is automatically split into 7-day chunks
internally (an Xray API limit) and merged into one CSV — you don't need to do
anything special for wide date ranges.

### Examples

Last 7 days, everything:
```bash
jf curation-plugin ce
```

A specific month, blocked packages only:
```bash
jf curation-plugin ce --from=2026-08-01 --to=2026-08-31 --status=blocked
```

Approved and not-inspected only, for a specific project:
```bash
jf curation-plugin ce --project=myproj --status=approved,not-inspected
```

Include dry-run policy simulations alongside real blocked events:
```bash
jf curation-plugin ce --status=blocked,dry-run
```

## CSV columns

```
id, created_at, action, reason, package_type, package_name, package_version, package_url,
curated_repo_server_name, curated_repo_name, curated_repo_project_key, user_name, user_mail,
origin_repo_server_name, origin_repo_name, origin_repo_project_key, public_repo_name, public_repo_url,
ecosystem, ondemand, policy_id, policy_name, condition_name, condition_category, dry_run, result, policy_reason
```

Matches the native Xray Curation audit CSV export format column-for-column.
