# krabber-net

Join the abyss. The Krabber website: a Go web server rendering htmx pages, backed
by one DynamoDB table, deployed to Elastic Beanstalk behind CloudFront.
See [PLAN.md](PLAN.md) for the architecture, security design and roadmap.

## Run it locally

Needs Go (the toolchain in `go.mod` downloads itself) and Docker.

```bash
make dev    # DynamoDB Local + table + sample krabs, then the site on http://localhost:5050
```

Sign in as `mrkrabs@krabber.test`, `spongebob@krabber.test` or `plankton@krabber.test`
with password `crabcakes123`. Emails (activation links) print to the server log.
`spongebob` is an admin and `mrkrabs` a moderator, so both see Krabmin (`/krabmin`).

Roles live on the account and can only be granted with `crabctl`, never from the site.
For example, this makes you the admin in prod:

```bash
AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/crabctl role <username> admin
```

Moderators can then be added or removed by an admin in Krabmin.

## Test

```bash
make db      # DynamoDB Local on :8000 (integration tests need it)
make test    # everything, with -race
make lint    # golangci-lint
make vuln    # govulncheck
```

Tests that need DynamoDB Local are skipped when `KRABBER_TEST_DYNAMO_ENDPOINT` isn't set.

## Layout

| Path | What |
|---|---|
| `cmd/web` | The website (Beanstalk runs this) |
| `cmd/devseed` | Dev only: create the table in DynamoDB Local and add sample krabs |
| `cmd/crabctl` | Operator commands run from your machine (set a crab's role) |
| `internal/store` | All DynamoDB access; every key format is in `keys.go` |
| `internal/web` | Routes, handlers, templates, security middleware |
| `internal/jobs` | Trench fan-out worker and sea refresh |
| `internal/mail` | Email templates, SES sender, daily cap |
| `internal/config` | Environment + SSM Parameter Store settings |
| `ui/` | Embedded HTML templates and static assets |
| `infra/` | Terraform (`bootstrap/` is applied once, by hand) |

## Configuration

| Variable | Meaning |
|---|---|
| `APP_ENV` | `prod` (default) or `dev` |
| `TABLE_NAME` | DynamoDB table (required) |
| `BASE_URL` | Public URL, `https://krabber.net` by default |
| `SSM_PREFIX` | If set (for example `/krabber/prod`), parameters under it override variables: `origin_verify_secret` becomes `ORIGIN_VERIFY_SECRET`, and so on |
| `ORIGIN_VERIFY_SECRET`, `ORIGIN_VERIFY_SECRET_PREVIOUS` | Value(s) of CloudFront's `X-Origin-Verify` header; required in prod |
| `MAIL_FROM`, `MAIL_DAILY_CAP`, `MAIL_CONFIGURATION_SET` | Email sender, daily limit (500), SES configuration set |
| `CONTACT_EMAIL` | Address shown on the terms and privacy pages and in ban emails; left out when unset |
| `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET` | Cloudflare Turnstile on signup and resend; off when unset |
| `DYNAMO_ENDPOINT` | DynamoDB Local URL; dev only |
| `PORT` | Listen port, 5000 by default (Beanstalk's) |

## Build for Beanstalk

```bash
make bundle   # dist/krabber.zip: bin/application (linux/arm64) + Procfile + .platform/
```
