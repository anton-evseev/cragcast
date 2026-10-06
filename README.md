# cragcast

## Publish static pages

Install Docker and Wrangler on the machine running the script, and build the Docker image. Put `wrangler.jsonc` in the repository root with `assets.directory` set to `./dist`.

Create a Cloudflare API token with **Edit Cloudflare Workers** permission, scoped to the target account. Do not put the token in the repository.

Set these variables in the environment of the scheduled task:

- `IMAGE`: Docker image name and tag.
- `WORK_DIR`: writable directory for each run's files.
- `CLOUDFLARE_API_TOKEN`: Cloudflare API token.
- `CLOUDFLARE_ACCOUNT_ID`: Cloudflare account ID.

`REGION` is optional and defaults to `seattle`.

Run `publish-cloudflare.sh` on a schedule. It scrapes the forecast, bundles the static pages, and deploys them with Wrangler. If a command fails, it stops before the next step.
