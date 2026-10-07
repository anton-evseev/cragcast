# cragcast

## Publish static pages

Install Docker and build the image from the repository root:

```sh
docker build -t cragcast:local .
```

The image includes the Go program, Wrangler, and `wrangler.jsonc`. The machine running the script does not need Node or Wrangler installed.

Create a Cloudflare API token with **Edit Cloudflare Workers** permission, scoped to the target account. Do not put the token in the repository.

Set these variables in the environment of the scheduled task:

- `IMAGE`: Docker image name and tag.
- `WORK_DIR`: writable directory for each run's files.
- `CLOUDFLARE_API_TOKEN`: Cloudflare API token.
- `CLOUDFLARE_ACCOUNT_ID`: Cloudflare account ID.

`REGION` is optional and defaults to `seattle`.

Run `publish-cloudflare.sh` on a schedule. It uses the image to scrape the forecast, bundle the static pages, and deploy them with Wrangler. If a command fails, it stops before the next step.
