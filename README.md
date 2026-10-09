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

Roles live on the account and can only be granted with `krabctl`, never from the site.
For example, this makes you the admin in prod:

```bash
AWS_PROFILE=krabber-admin TABLE_NAME=krabber-prod go run ./cmd/krabctl role <username> admin
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

`TestCostProfile` (`internal/web/cost_test.go`) measures the DynamoDB read and write units of the main pages, polls and actions, and fails when one goes over its budget. To see the table, and to turn it into a monthly bill:

```bash
KRABBER_TEST_DYNAMO_ENDPOINT=http://localhost:8000 go test -run TestCostProfile -v ./internal/web/
python3 scripts/cost_model.py 1000 5000 10000   # daily active krabs
```

## Layout

| Path | What |
|---|---|
| `cmd/web` | The website (Beanstalk runs this) |
| `cmd/devseed` | Dev only: create the table in DynamoDB Local and add sample krabs |
| `cmd/krabctl` | Operator commands run from your machine (set a krab's role, verify a krab) |
| `scripts/create_table.py` | Create the DynamoDB table with boto3 (`--local` for DynamoDB Local, `--print` for the JSON definition); a Go test keeps it matching `internal/store/schema.go` |
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
| `SIGNUP_MODE` | `open` (default; an invite code is optional), `invite` (a krab's invite code is required) or `closed` |
| `MAX_KRABS` | Close signups once this many krabs can sign in (0, the default, means no cap); see the cost table in PLAN.md section 2.2 |
| `LOG_ALL_REQUESTS` | `true` logs every request with its DynamoDB read and write units, instead of 5% of successful ones (dev always logs all) |
| `DYNAMO_ENDPOINT` | DynamoDB Local URL; dev only |
| `PORT` | Listen port, 5000 by default (Beanstalk's) |

## Build for Beanstalk

```bash
make bundle   # dist/krabber.zip: bin/application (linux/arm64) + Procfile + .platform/
```

## Deploy

Deploys run from a laptop, which costs no GitHub Actions minutes:

```bash
aws login --profile krabber-admin
export AWS_PROFILE=krabber-admin
make plan && make apply   # only when infra/ changed; review the plan first
make deploy               # checks, ships the pushed commit, waits until healthy, publishes a GitHub release
```

`make deploy` refuses to run with uncommitted or unpushed changes (the release has to match a commit GitHub has) and runs the public check first. It tags the commit `vYYYY.MM.DD-HHMM` and creates a GitHub release with generated notes; that needs a working `gh` (`gh auth status`). Away from the laptop, the **deploy** workflow in the Actions tab does the same deploy without the release.

## Before going public

```bash
make public-check   # nothing private tracked or anywhere in the history
make hooks          # run that check before every git push
```

## License and credits

Krabber is licensed under the [GNU General Public License v2.0](LICENSE).

Krabber is a Go port of [Crabber](https://github.com/crabber-net/crabber)
([crabber.net](https://crabber.net)), an open-source Flask application, which is
itself GPL-2.0. Krabber keeps that license and credits Crabber's original
authors. It is a separate, independent project — not affiliated with or endorsed
by the Crabber maintainers — and its name, logo and icons are its own.
